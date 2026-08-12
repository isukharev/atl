package agenteval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/isukharev/atl/internal/agenteval/executionbackend"
	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

const (
	ExecutionBackendContractSchema     = executionbackend.ContractSchema
	ExecutionBackendTrialPlanSchema    = executionbackend.PlanSchema
	ExecutionBackendTrialReceiptSchema = executionbackend.ReceiptSchema
	ExecutionBackendSchemaVersion      = executionbackend.SchemaVersion
	ExecutionBackendContractMaxBytes   = executionbackend.MaxContractBytes
	ExecutionBackendPlanMaxBytes       = executionbackend.MaxPlanBytes
	ExecutionBackendReceiptMaxBytes    = executionbackend.MaxReceiptBytes
)

type ExecutionBackendContract = executionbackend.Contract
type ExecutionBackendTrialPlan = executionbackend.Plan
type ExecutionBackendTrialReceipt = executionbackend.Receipt
type ExecutionBackendReferencePlanOptions = executionbackend.ReferencePlanOptions
type ExecutionBackendReferenceInputs = executionbackend.ReferenceInputs
type ExecutionBackendReferenceResult = executionbackend.RunResult
type ExecutionBackendLocalProcessPlanOptions = executionbackend.LocalProcessPlanOptions

func DecodeExecutionBackendContract(reader io.Reader) (ExecutionBackendContract, error) {
	return executionbackend.DecodeContract(reader)
}

func EncodeExecutionBackendContract(contract ExecutionBackendContract) ([]byte, error) {
	return executionbackend.EncodeContract(contract)
}

func DecodeExecutionBackendTrialPlan(reader io.Reader) (ExecutionBackendTrialPlan, error) {
	return executionbackend.DecodePlan(reader)
}

func EncodeExecutionBackendTrialPlan(plan ExecutionBackendTrialPlan) ([]byte, error) {
	return executionbackend.EncodePlan(plan)
}

func DecodeExecutionBackendTrialReceipt(reader io.Reader, plan ExecutionBackendTrialPlan) (ExecutionBackendTrialReceipt, error) {
	return executionbackend.DecodeReceipt(reader, plan)
}

func EncodeExecutionBackendTrialReceipt(plan ExecutionBackendTrialPlan, receipt ExecutionBackendTrialReceipt) ([]byte, error) {
	return executionbackend.EncodeReceipt(plan, receipt)
}

func HermeticReferenceExecutionBackendContract() (ExecutionBackendContract, error) {
	return executionbackend.ReferenceContract()
}

func LocalProcessExecutionBackendContract(implementationSHA256, executableSHA256 string) (ExecutionBackendContract, error) {
	return executionbackend.LocalProcessContract(implementationSHA256, executableSHA256)
}

func NewLocalProcessExecutionBackendTrialPlan(contract ExecutionBackendContract, options ExecutionBackendLocalProcessPlanOptions) (ExecutionBackendTrialPlan, error) {
	return executionbackend.NewLocalProcessPlan(contract, options)
}

func NewHermeticReferenceTrialPlan(contract ExecutionBackendContract, options ExecutionBackendReferencePlanOptions) (ExecutionBackendTrialPlan, error) {
	return executionbackend.NewReferencePlan(contract, options)
}

func BindExecutionBackendTrial(binding lifecycle.Binding, plan ExecutionBackendTrialPlan) (lifecycle.Binding, error) {
	digest, err := executionbackend.PlanSHA256(plan)
	if err != nil {
		return lifecycle.Binding{}, err
	}
	binding.Identity.EnvironmentSHA256 = digest
	return binding, nil
}

// RunHermeticReferenceTrial executes the closed in-memory reference substrate.
// Admission completes while the durable attempt is still planned. The
// reference has no process tree; after backend entry, definitive terminal
// states are emitted only after the synchronous action has stopped and its
// logical state has been closed.
func RunHermeticReferenceTrial(ctx context.Context, session *DurableAttemptSession, plan ExecutionBackendTrialPlan, inputs ExecutionBackendReferenceInputs) (ExecutionBackendReferenceResult, error) {
	if session == nil {
		return ExecutionBackendReferenceResult{}, fmt.Errorf("hermetic reference trial requires a durable attempt")
	}
	if ctx == nil {
		err := fmt.Errorf("hermetic reference trial requires a context")
		return ExecutionBackendReferenceResult{}, joinAttemptLifecycleError(err, session.PolicyDenied())
	}
	contract, err := executionbackend.ReferenceContract()
	if err != nil {
		return ExecutionBackendReferenceResult{}, err
	}
	admitted, err := executionbackend.Admit(contract, plan)
	if err != nil {
		if errors.Is(err, executionbackend.ErrUnsupported) {
			return ExecutionBackendReferenceResult{}, joinAttemptLifecycleError(err, session.Unsupported())
		}
		return ExecutionBackendReferenceResult{}, joinAttemptLifecycleError(err, session.PolicyDenied())
	}
	if admitted.SHA256() != session.plan.Binding.Identity.EnvironmentSHA256 {
		return ExecutionBackendReferenceResult{}, joinAttemptLifecycleError(fmt.Errorf("execution backend attempt binding changed"), session.PolicyDenied())
	}
	// Plan admission proves DeadlineMillis is in 1..MaxDeadlineMillis.
	trialContext, cancelTrial := context.WithTimeout(ctx, time.Duration(plan.Resources.DeadlineMillis)*time.Millisecond) // #nosec G115 -- bounded before conversion.
	defer cancelTrial()
	ownedInputs, err := executionbackend.PrepareReferenceInputs(trialContext, admitted, inputs)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(trialContext.Err(), context.DeadlineExceeded) {
			return ExecutionBackendReferenceResult{}, joinAttemptLifecycleError(err, session.Timeout(false, lifecycle.UnknownUsage()))
		}
		if errors.Is(err, context.Canceled) || errors.Is(trialContext.Err(), context.Canceled) {
			return ExecutionBackendReferenceResult{}, joinAttemptLifecycleError(err, session.Cancel(false, lifecycle.UnknownUsage()))
		}
		return ExecutionBackendReferenceResult{}, joinAttemptLifecycleError(err, session.PolicyDenied())
	}
	defer clearExecutionBackendReferenceInputs(&ownedInputs)
	if err := beginRunAttempt(session); err != nil {
		return ExecutionBackendReferenceResult{}, err
	}
	result, runErr := executionbackend.RunReference(trialContext, admitted, ownedInputs)
	usage := lifecycle.UnknownUsage()
	if runErr != nil && errors.Is(runErr, executionbackend.ErrInterrupted) {
		var lifecycleErr error
		switch {
		case errors.Is(runErr, context.DeadlineExceeded):
			lifecycleErr = session.Timeout(true, usage)
		case errors.Is(ctx.Err(), context.Canceled), errors.Is(runErr, context.Canceled):
			lifecycleErr = session.Cancel(true, usage)
		default:
			lifecycleErr = session.Unknown(lifecycle.ErrorInternal, usage)
		}
		return result, joinAttemptLifecycleError(runErr, lifecycleErr)
	}
	if runErr != nil {
		receipt, digestErr := contentMinimizedAttemptDigest("execution-backend-failure", struct {
			Code       string `json:"code"`
			PlanSHA256 string `json:"plan_sha256"`
		}{"execution_failed", admitted.SHA256()})
		if digestErr != nil {
			return result, joinAttemptLifecycleError(runErr, session.Unknown(lifecycle.ErrorInternal, usage))
		}
		return result, joinAttemptLifecycleError(runErr, session.Fail(receipt, usage))
	}
	receipt, err := executionbackend.ReceiptSHA256(plan, result.Receipt)
	if err != nil {
		return result, joinAttemptLifecycleError(err, session.Unknown(lifecycle.ErrorInternal, usage))
	}
	switch result.Receipt.Verdict {
	case executionbackend.VerdictSucceeded:
		return result, joinAttemptLifecycleError(nil, session.Succeed(receipt, usage))
	case executionbackend.VerdictFailed:
		return result, joinAttemptLifecycleError(nil, session.Fail(receipt, usage))
	default:
		err := fmt.Errorf("execution backend returned an unsafe terminal verdict")
		return result, joinAttemptLifecycleError(err, session.Unknown(lifecycle.ErrorInternal, usage))
	}
}

func clearExecutionBackendReferenceInputs(inputs *ExecutionBackendReferenceInputs) {
	if inputs == nil {
		return
	}
	clear(inputs.Fixture)
	clear(inputs.Skill)
	clear(inputs.Definitions)
	inputs.Fixture = nil
	inputs.Skill = nil
	inputs.Definitions = nil
}
