package mirror

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

func validatePublicationArtifact(artifact completePullPublicationArtifact, stageDir string, allowPrivate bool, token string, writeIndex int) (ArtifactPath, error) {
	path, err := artifactPathFromDurable(artifact.Path)
	if err != nil || (!allowPrivate && !artifactPathIsPublic(path)) {
		return ArtifactPath{}, fmt.Errorf("invalid publication path")
	}
	if artifact.Pre.Present {
		mode := os.FileMode(artifact.Pre.Mode)
		if !validSHA256(artifact.Pre.SHA256) || mode != mode.Perm() || mode.Perm() == 0 {
			return ArtifactPath{}, fmt.Errorf("invalid publication pre-image")
		}
	} else if artifact.Pre.SHA256 != "" || artifact.Pre.Mode != 0 {
		return ArtifactPath{}, fmt.Errorf("absent publication pre-image has a hash")
	}
	if artifact.Remove {
		if artifact.BestEffort || artifact.Payload != "" || artifact.SHA256 != "" || artifact.Size != 0 || artifact.Mode != 0 || artifact.Temp != "" {
			return ArtifactPath{}, fmt.Errorf("invalid publication removal")
		}
		return path, nil
	}
	if artifact.Payload == "" || filepath.Base(artifact.Payload) != artifact.Payload || strings.ContainsAny(artifact.Payload, "/\\:\x00") {
		return ArtifactPath{}, fmt.Errorf("invalid publication payload name")
	}
	if !validSHA256(artifact.SHA256) || artifact.Size < 0 || artifact.Size > maxCompletePullPublicationBytes {
		return ArtifactPath{}, fmt.Errorf("invalid publication payload identity")
	}
	mode := os.FileMode(artifact.Mode)
	if mode != mode.Perm() || mode.Perm() == 0 {
		return ArtifactPath{}, fmt.Errorf("invalid publication artifact mode")
	}
	if artifact.Temp != completePullArtifactTemp(token, writeIndex) || !validCompletePullTempName(artifact.Temp) {
		return ArtifactPath{}, fmt.Errorf("invalid publication temporary-file ownership")
	}
	if stageDir != "" {
		payloadPath := filepath.Join(stageDir, artifact.Payload)
		info, err := safepath.StatWithin(filepath.Dir(stageDir), payloadPath)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != artifact.Size {
			return ArtifactPath{}, fmt.Errorf("publication payload is missing or has unsafe metadata")
		}
		payload, err := safepath.ReadFileWithin(filepath.Dir(stageDir), payloadPath)
		if err != nil || Hash(payload) != artifact.SHA256 {
			return ArtifactPath{}, fmt.Errorf("publication payload is missing or changed")
		}
	}
	return path, nil
}

func publicationCurrent(root string, path ArtifactPath) (completePullPublicationPreState, error) {
	target, pathErr := artifactPathTarget(root, path)
	if pathErr != nil {
		return completePullPublicationPreState{}, pathErr
	}
	info, err := safepath.StatWithin(root, target)
	if os.IsNotExist(err) {
		return completePullPublicationPreState{}, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return completePullPublicationPreState{}, fmt.Errorf("inspect publication destination: %v", err)
	}
	b, err := safepath.ReadFileWithin(root, target)
	if err != nil {
		return completePullPublicationPreState{}, err
	}
	return completePullPublicationPreState{Present: true, SHA256: Hash(b), Mode: uint32(info.Mode().Perm())}, nil
}

func publicationMatchesPre(current, pre completePullPublicationPreState) bool {
	return current == pre
}

func publicationMatchesPost(current completePullPublicationPreState, artifact completePullPublicationArtifact) bool {
	if artifact.Remove || (artifact.BestEffort && !current.Present) {
		return !current.Present
	}
	return current.Present && current.SHA256 == artifact.SHA256 && current.Mode == artifact.Mode
}

func (m *Mirror) stagePublicationArtifact(dir string, input CompletePullArtifact, sequence int, token string, ops completePullPublicationOps) (completePullPublicationArtifact, error) {
	if err := validateArtifactPath(input.Path); err != nil {
		return completePullPublicationArtifact{}, fmt.Errorf("%w: invalid complete-pull destination", domain.ErrCheckFailed)
	}
	if artifactPathIsPrivateBase(input.Path) && (input.Remove || input.BestEffort || input.Mode != 0o600) {
		return completePullPublicationArtifact{}, fmt.Errorf("%w: invalid private complete-pull artifact", domain.ErrCheckFailed)
	}
	pre, err := publicationCurrent(m.Root, input.Path)
	if err != nil {
		return completePullPublicationArtifact{}, fmt.Errorf("%w: %v", domain.ErrCheckFailed, err)
	}
	durablePath, err := artifactPathDurableString(input.Path)
	if err != nil {
		return completePullPublicationArtifact{}, err
	}
	out := completePullPublicationArtifact{Path: durablePath, Pre: pre, Remove: input.Remove, BestEffort: input.BestEffort}
	if input.Remove {
		if input.BestEffort || len(input.Data) != 0 || input.Mode != 0 {
			return completePullPublicationArtifact{}, fmt.Errorf("%w: invalid complete-pull removal", domain.ErrCheckFailed)
		}
		return out, nil
	}
	if input.Mode != input.Mode.Perm() || input.Mode.Perm() == 0 {
		return completePullPublicationArtifact{}, fmt.Errorf("%w: invalid complete-pull mode", domain.ErrCheckFailed)
	}
	out.Payload = fmt.Sprintf("payload-%04d", sequence)
	out.Temp = completePullArtifactTemp(token, sequence)
	out.SHA256 = Hash(input.Data)
	out.Size = int64(len(input.Data))
	out.Mode = uint32(input.Mode.Perm())
	if err := ops.write(m.Root, filepath.Join(dir, out.Payload), input.Data, 0o600); err != nil {
		return completePullPublicationArtifact{}, err
	}
	return out, nil
}
