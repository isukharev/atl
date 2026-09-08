package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"

	"github.com/isukharev/atl/scripts/check-docs-freshness/hosted"
)

func selectPlan(ctx context.Context, b binding) (hosted.Plan, error) {
	args := []string{"run", "./scripts/check-docs-freshness", "-hosted-plan"}
	if b.Full {
		args = append(args, "-full")
	}
	command := exec.CommandContext(ctx, "go", args...)
	command.Env = append(os.Environ(), "ATL_DOCS_BASE="+b.Base, "ATL_DOCS_HEAD="+b.Head)
	body, err := command.Output()
	if err != nil {
		return hosted.Plan{}, errors.New("committed hosted impact selection failed")
	}
	return decodePlan(body, b)
}

func decodePlan(body []byte, b binding) (hosted.Plan, error) {
	var plan hosted.Plan
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if len(body) > 8192 || decoder.Decode(&plan) != nil || decoder.Decode(new(any)) != io.EOF ||
		plan.SchemaVersion != 1 || plan.BaseSHA != b.Base || plan.HeadSHA != b.Head ||
		!plan.Has("contracts") || !slices.IsSorted(plan.Lanes) || b.Full && !plan.Full {
		return hosted.Plan{}, errors.New("invalid or unbound hosted impact plan")
	}
	for index, lane := range plan.Lanes {
		if !slices.Contains(hosted.Lanes, lane) || index > 0 && lane == plan.Lanes[index-1] {
			return hosted.Plan{}, errors.New("unknown or repeated hosted lane")
		}
	}
	if plan.Full && !slices.Equal(plan.Lanes, hosted.Lanes) ||
		plan.Has("product") && (!plan.Has("eval-compat") || !plan.Has("security")) ||
		plan.Has("eval-full") != plan.Has("platform") || plan.Has("eval-full") && !plan.Has("security") {
		return hosted.Plan{}, errors.New("hosted plan omits implied module gates")
	}
	return plan, nil
}

func planOutputs(plan hosted.Plan) (map[string]string, error) {
	body, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"plan": string(body), "product": strconv.FormatBool(plan.Has("product")),
		"evaluator": plan.Evaluator(), "platform": strconv.FormatBool(plan.Has("platform")),
		"security": strconv.FormatBool(plan.Has("security")), "corpus": strconv.FormatBool(plan.Has("corpus")),
	}, nil
}

func publishPlan(plan hosted.Plan, path string) error {
	outputs, err := planOutputs(plan)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return errors.New("cannot open workflow output file")
	}
	defer file.Close()
	for _, key := range []string{"plan", "product", "evaluator", "platform", "security", "corpus"} {
		if _, err := fmt.Fprintf(file, "%s=%s\n", key, outputs[key]); err != nil {
			return err
		}
	}
	// The plan contains only public commit identities and closed lane names.
	fmt.Println(outputs["plan"])
	return nil
}
