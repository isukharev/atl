package app

import (
	"context"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/scmref"
)

const (
	jiraDevelopmentMaxProjects      = 64
	jiraDevelopmentMaxCommits       = 256
	jiraDevelopmentMaxBranches      = 128
	jiraDevelopmentMaxMergeRequests = 128
	jiraDevelopmentMaxArtifacts     = 512
	jiraDevelopmentMaxURLBytes      = 2048
	jiraDevelopmentMaxBranchBytes   = 512
)

var (
	jiraDevelopmentSHA            = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	jiraDevelopmentSourceID       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	jiraDevelopmentIID            = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
)

type jiraDevelopmentProjection struct {
	projects []domain.JiraDevelopmentProject
	commits  []domain.JiraDevelopmentCommit
	branches []domain.JiraDevelopmentBranch
	mrs      []domain.JiraDevelopmentMergeRequest
}

func (b *jiraGraphBuilder) collectDevelopment(ctx context.Context, tracker domain.Tracker, numericIssueID string) error {
	source := b.sources["development"]
	reader, ok := tracker.(domain.JiraDevelopmentReader)
	if !ok {
		source.Status = domain.ArtifactSourceUnsupported
		source.Complete = false
		return nil
	}
	inventory, err := reader.ReadIssueDevelopment(ctx, numericIssueID)
	if err != nil {
		return b.qualifyAuxiliaryError(ctx, source, err, true)
	}
	projection, ok := validateJiraDevelopmentInventory(inventory)
	if !ok {
		b.markMalformed(source)
		return nil
	}
	source.Count = len(projection.commits) + len(projection.branches) + len(projection.mrs)

	for _, project := range projection.projects {
		b.addDevelopmentFact(source, "development_project", project, "", "", "", "")
	}
	for _, commit := range projection.commits {
		b.addDevelopmentFact(source, "development_commit",
			domain.JiraDevelopmentProject{Host: commit.Host, ProjectPath: commit.ProjectPath}, commit.SHA, "", "", "")
	}
	for _, branch := range projection.branches {
		b.addDevelopmentFact(source, "development_branch",
			domain.JiraDevelopmentProject{Host: branch.Host, ProjectPath: branch.ProjectPath}, "", branch.Name, "", "")
	}
	for _, mr := range projection.mrs {
		b.addDevelopmentFact(source, "development_merge_request",
			domain.JiraDevelopmentProject{Host: mr.Host, ProjectPath: mr.ProjectPath}, "", "", mr.IID, mr.State)
	}
	b.completeSource(source)
	return nil
}

func (b *jiraGraphBuilder) addDevelopmentFact(source *domain.ArtifactGraphSource, edgeKind string, project domain.JiraDevelopmentProject, sha, branch, iid, mrState string) {
	projectHash := graphHash("https://" + project.Host + "\x00" + project.ProjectPath)
	scm := &domain.ArtifactGraphSCMIdentity{Host: project.Host, ProjectPath: project.ProjectPath}
	nodeKind := strings.TrimPrefix(edgeKind, "development_")
	nodeID := "gitlab:" + nodeKind + ":" + projectHash
	switch edgeKind {
	case "development_commit":
		scm.CommitSHA = sha
		nodeID += ":" + sha
	case "development_branch":
		scm.BranchName = branch
		nodeID += ":" + graphHash(branch)
	case "development_merge_request":
		scm.MergeRequestIID, scm.MergeRequestState = iid, mrState
		nodeID += ":" + iid
	}
	node := domain.ArtifactGraphNode{
		ID: nodeID, Kind: "gitlab_" + nodeKind, Service: "gitlab",
		URL: jiraDevelopmentWebURL(project, sha, branch, iid), State: domain.ArtifactNodeStub,
		Depth: 1, Stability: domain.ArtifactStabilityExperimentalAPI, SCM: scm,
	}
	if !b.addNode(node, source) {
		return
	}
	b.addEdge(domain.ArtifactGraphEdge{
		From: b.result.RootID, To: node.ID, Kind: edgeKind, Direction: "outbound",
		Current: true, Confidence: "exact", Stability: domain.ArtifactStabilityExperimentalAPI,
		Evidence: []domain.ArtifactGraphEvidence{{
			Collector: "development", SourceKind: "development_detail",
			SourceID: jiraDevelopmentEvidenceSourceID(edgeKind, scm), Extraction: "structured",
		}},
	}, source)
}

func jiraDevelopmentEvidenceSourceID(edgeKind string, scm *domain.ArtifactGraphSCMIdentity) string {
	if scm == nil {
		return ""
	}
	identity := strings.Join([]string{
		edgeKind, scm.Host, scm.ProjectPath, scm.CommitSHA,
		scm.BranchName, scm.MergeRequestIID,
	}, "\x00")
	return graphHash(identity)
}

func validateJiraDevelopmentInventory(in domain.JiraDevelopmentInventory) (jiraDevelopmentProjection, bool) {
	if len(in.Projects) > jiraDevelopmentMaxProjects || len(in.Commits) > jiraDevelopmentMaxCommits ||
		len(in.Branches) > jiraDevelopmentMaxBranches || len(in.MergeRequests) > jiraDevelopmentMaxMergeRequests ||
		len(in.Commits)+len(in.Branches)+len(in.MergeRequests) > jiraDevelopmentMaxArtifacts {
		return jiraDevelopmentProjection{}, false
	}
	out := jiraDevelopmentProjection{
		projects: append([]domain.JiraDevelopmentProject(nil), in.Projects...),
		commits:  append([]domain.JiraDevelopmentCommit(nil), in.Commits...),
		branches: append([]domain.JiraDevelopmentBranch(nil), in.Branches...),
		mrs:      append([]domain.JiraDevelopmentMergeRequest(nil), in.MergeRequests...),
	}
	projects := map[string]bool{}
	for _, project := range out.projects {
		key, ok := jiraDevelopmentProjectKey(project.Host, project.ProjectPath)
		if !ok || projects[key] {
			return jiraDevelopmentProjection{}, false
		}
		projects[key] = true
	}
	referenced := map[string]bool{}
	commits := map[string]bool{}
	for index := range out.commits {
		out.commits[index].SHA = strings.ToLower(out.commits[index].SHA)
		projectKey, ok := jiraDevelopmentProjectKey(out.commits[index].Host, out.commits[index].ProjectPath)
		identity := projectKey + "\x00" + out.commits[index].SHA
		project := domain.JiraDevelopmentProject{Host: out.commits[index].Host, ProjectPath: out.commits[index].ProjectPath}
		if !ok || !projects[projectKey] || !jiraDevelopmentSHA.MatchString(out.commits[index].SHA) || commits[identity] ||
			len(jiraDevelopmentWebURL(project, out.commits[index].SHA, "", "")) > jiraDevelopmentMaxURLBytes {
			return jiraDevelopmentProjection{}, false
		}
		commits[identity], referenced[projectKey] = true, true
	}
	branches := map[string]bool{}
	for _, branch := range out.branches {
		projectKey, ok := jiraDevelopmentProjectKey(branch.Host, branch.ProjectPath)
		identity := projectKey + "\x00" + branch.Name
		project := domain.JiraDevelopmentProject{Host: branch.Host, ProjectPath: branch.ProjectPath}
		if !ok || !projects[projectKey] || !jiraDevelopmentBranch(branch.Name) || branches[identity] ||
			len(jiraDevelopmentWebURL(project, "", branch.Name, "")) > jiraDevelopmentMaxURLBytes {
			return jiraDevelopmentProjection{}, false
		}
		branches[identity], referenced[projectKey] = true, true
	}
	mrs := map[string]bool{}
	for _, mr := range out.mrs {
		projectKey, ok := jiraDevelopmentProjectKey(mr.Host, mr.ProjectPath)
		identity := projectKey + "\x00" + mr.IID
		project := domain.JiraDevelopmentProject{Host: mr.Host, ProjectPath: mr.ProjectPath}
		if !ok || !projects[projectKey] || !jiraDevelopmentIID.MatchString(mr.IID) ||
			!oneOf(mr.State, "open", "merged", "closed", "unknown") || mrs[identity] ||
			len(jiraDevelopmentWebURL(project, "", "", mr.IID)) > jiraDevelopmentMaxURLBytes {
			return jiraDevelopmentProjection{}, false
		}
		mrs[identity], referenced[projectKey] = true, true
	}
	if len(referenced) != len(projects) {
		return jiraDevelopmentProjection{}, false
	}
	sort.Slice(out.projects, func(i, j int) bool {
		return out.projects[i].Host+"\x00"+out.projects[i].ProjectPath < out.projects[j].Host+"\x00"+out.projects[j].ProjectPath
	})
	sort.Slice(out.commits, func(i, j int) bool {
		return out.commits[i].Host+"\x00"+out.commits[i].ProjectPath+"\x00"+out.commits[i].SHA < out.commits[j].Host+"\x00"+out.commits[j].ProjectPath+"\x00"+out.commits[j].SHA
	})
	sort.Slice(out.branches, func(i, j int) bool {
		return out.branches[i].Host+"\x00"+out.branches[i].ProjectPath+"\x00"+out.branches[i].Name < out.branches[j].Host+"\x00"+out.branches[j].ProjectPath+"\x00"+out.branches[j].Name
	})
	sort.Slice(out.mrs, func(i, j int) bool {
		return out.mrs[i].Host+"\x00"+out.mrs[i].ProjectPath+"\x00"+out.mrs[i].IID < out.mrs[j].Host+"\x00"+out.mrs[j].ProjectPath+"\x00"+out.mrs[j].IID
	})
	return out, true
}

func jiraDevelopmentProjectKey(host, projectPath string) (string, bool) {
	project, ok := scmref.ValidateGitLabProject(host, projectPath)
	if !ok {
		return "", false
	}
	return project.Host + "\x00" + project.ProjectPath, true
}

func jiraDevelopmentBranch(value string) bool {
	if value == "" || len(value) > jiraDevelopmentMaxBranchBytes || !utf8.ValidString(value) {
		return false
	}
	for _, current := range value {
		if current == 0 || unicode.IsControl(current) {
			return false
		}
	}
	return true
}

func jiraDevelopmentWebURL(project domain.JiraDevelopmentProject, sha, branch, iid string) string {
	escapeProject := func(value string) string {
		parts := strings.Split(value, "/")
		for index := range parts {
			parts[index] = url.PathEscape(parts[index])
		}
		return strings.Join(parts, "/")
	}
	base := "https://" + project.Host + "/" + escapeProject(project.ProjectPath)
	switch {
	case sha != "":
		return base + "/-/commit/" + sha
	case branch != "":
		return base + "/-/tree/" + url.PathEscape(branch)
	case iid != "":
		return base + "/-/merge_requests/" + iid
	default:
		return base
	}
}

func validateJiraDevelopmentGraphNode(node domain.ArtifactGraphNode) bool {
	if node.SCM == nil || node.Service != "gitlab" || node.ExternalID != "" || node.Label != "" ||
		node.State != domain.ArtifactNodeStub || node.Expanded || node.Depth < 1 ||
		node.Stability != domain.ArtifactStabilityExperimentalAPI || len(node.URL) > jiraDevelopmentMaxURLBytes {
		return false
	}
	scm := node.SCM
	if _, ok := jiraDevelopmentProjectKey(scm.Host, scm.ProjectPath); !ok {
		return false
	}
	project := domain.JiraDevelopmentProject{Host: scm.Host, ProjectPath: scm.ProjectPath}
	projectHash := graphHash("https://" + scm.Host + "\x00" + scm.ProjectPath)
	switch node.Kind {
	case "gitlab_project":
		return scm.CommitSHA == "" && scm.BranchName == "" && scm.MergeRequestIID == "" && scm.MergeRequestState == "" &&
			node.ID == "gitlab:project:"+projectHash && node.URL == jiraDevelopmentWebURL(project, "", "", "")
	case "gitlab_commit":
		return jiraDevelopmentSHA.MatchString(scm.CommitSHA) && scm.BranchName == "" && scm.MergeRequestIID == "" && scm.MergeRequestState == "" &&
			node.ID == "gitlab:commit:"+projectHash+":"+scm.CommitSHA &&
			node.URL == jiraDevelopmentWebURL(project, scm.CommitSHA, "", "")
	case "gitlab_branch":
		return scm.CommitSHA == "" && jiraDevelopmentBranch(scm.BranchName) && scm.MergeRequestIID == "" && scm.MergeRequestState == "" &&
			node.ID == "gitlab:branch:"+projectHash+":"+graphHash(scm.BranchName) &&
			node.URL == jiraDevelopmentWebURL(project, "", scm.BranchName, "")
	case "gitlab_merge_request":
		return scm.CommitSHA == "" && scm.BranchName == "" && jiraDevelopmentIID.MatchString(scm.MergeRequestIID) &&
			oneOf(scm.MergeRequestState, "open", "merged", "closed", "unknown") &&
			node.ID == "gitlab:merge_request:"+projectHash+":"+scm.MergeRequestIID &&
			node.URL == jiraDevelopmentWebURL(project, "", "", scm.MergeRequestIID)
	default:
		return false
	}
}
