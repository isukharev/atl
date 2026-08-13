package corpus

func validateRetentionPlan(plan RetentionPlanV1, limits Limits, requireDigest bool) error {
	if plan.SchemaVersion != RetentionPlanSchemaV1 {
		return reject(ReasonSchema)
	}
	if !isLowerSHA256(plan.RootDigest) {
		return reject(ReasonDigest)
	}
	if err := validateRetentionPolicy(plan.Policy, limits); err != nil {
		return err
	}
	if _, err := canonicalPointer(plan.Current); err != nil {
		return err
	}
	if len(plan.Inventory) == 0 || len(plan.Inventory) > limits.MaxMembers ||
		len(plan.Protected) == 0 || len(plan.Protected) > plan.Policy.RetainPredecessors+1 ||
		len(plan.Candidates) > limits.MaxMembers || plan.UnsealedStages < 0 || plan.UnsealedStages > limits.MaxMembers ||
		!isLowerSHA256(plan.UnsealedInventoryDigest) {
		return reject(ReasonBounds)
	}
	recordsByID := make(map[string]RetentionInventoryRecordV1, len(plan.Inventory))
	recordsByDigest := make(map[string]RetentionInventoryRecordV1, len(plan.Inventory))
	for index, record := range plan.Inventory {
		if err := validGenerationID(record.GenerationID); err != nil || !isLowerSHA256(record.GenerationDigest) ||
			record.PredecessorGenerationDigest != "" && !isLowerSHA256(record.PredecessorGenerationDigest) ||
			record.PredecessorGenerationID != "" && validGenerationID(record.PredecessorGenerationID) != nil {
			return reject(ReasonDigest)
		}
		if record.PredecessorGenerationID != "" && (record.PredecessorGenerationDigest == "" || record.PredecessorGenerationID == record.GenerationID) {
			return reject(ReasonLineage)
		}
		if record.Totals.Members < 0 || record.Totals.Members > limits.MaxMembers || record.Totals.Bytes < 0 || record.Totals.Bytes > limits.MaxTotalBytes {
			return reject(ReasonBounds)
		}
		if index > 0 && plan.Inventory[index-1].GenerationID >= record.GenerationID {
			return reject(ReasonMembership)
		}
		if _, duplicate := recordsByDigest[record.GenerationDigest]; duplicate {
			return reject(ReasonDigest)
		}
		recordsByID[record.GenerationID] = record
		recordsByDigest[record.GenerationDigest] = record
	}
	if current, exists := recordsByID[plan.Current.GenerationID]; !exists || current.GenerationDigest != plan.Current.GenerationDigest {
		return reject(ReasonLineage)
	}
	partition := make(map[string]struct{}, len(plan.Inventory))
	for index, ref := range plan.Protected {
		record, exists := recordsByID[ref.GenerationID]
		if !exists || record.GenerationDigest != ref.GenerationDigest {
			return reject(ReasonMembership)
		}
		if _, duplicate := partition[ref.GenerationID]; duplicate {
			return reject(ReasonMembership)
		}
		partition[ref.GenerationID] = struct{}{}
		if index == 0 {
			if ref.GenerationID != plan.Current.GenerationID || ref.GenerationDigest != plan.Current.GenerationDigest {
				return reject(ReasonLineage)
			}
			continue
		}
		previous := recordsByID[plan.Protected[index-1].GenerationID]
		if previous.PredecessorGenerationDigest != ref.GenerationDigest ||
			previous.PredecessorGenerationID != "" && previous.PredecessorGenerationID != ref.GenerationID {
			return reject(ReasonLineage)
		}
	}
	last := recordsByID[plan.Protected[len(plan.Protected)-1].GenerationID]
	if len(plan.Protected) <= plan.Policy.RetainPredecessors && last.PredecessorGenerationDigest != "" {
		// A prior reviewed retention apply may have pruned an ancestor beyond
		// the retained active depth. The historical digest remains immutable
		// lineage, but no surviving sealed record may silently replace it.
		if _, exists := recordsByDigest[last.PredecessorGenerationDigest]; exists {
			return reject(ReasonLineage)
		}
	}
	for index, ref := range plan.Candidates {
		record, exists := recordsByID[ref.GenerationID]
		if !exists || record.GenerationDigest != ref.GenerationDigest {
			return reject(ReasonMembership)
		}
		if index > 0 && plan.Candidates[index-1].GenerationID >= ref.GenerationID {
			return reject(ReasonMembership)
		}
		if _, duplicate := partition[ref.GenerationID]; duplicate {
			return reject(ReasonMembership)
		}
		partition[ref.GenerationID] = struct{}{}
	}
	if len(partition) != len(plan.Inventory) {
		return reject(ReasonMembership)
	}
	if requireDigest {
		if !isLowerSHA256(plan.PlanDigest) {
			return reject(ReasonDigest)
		}
	} else if plan.PlanDigest != "" {
		return reject(ReasonDigest)
	}
	return nil
}

func validateRetentionPolicy(policy RetentionPolicyV1, limits Limits) error {
	if policy.SchemaVersion != RetentionPolicySchemaV1 {
		return reject(ReasonSchema)
	}
	if policy.RetainPredecessors < 1 || policy.RetainPredecessors > limits.MaxMembers-1 {
		return reject(ReasonBounds)
	}
	return nil
}

func retentionPlanDigest(plan RetentionPlanV1) (string, error) {
	projection := plan
	projection.PlanDigest = ""
	data, err := marshalCanonical(projection)
	if err != nil {
		return "", err
	}
	return domainHash(retentionPlanDigestDomain, data), nil
}

func retentionUnsealedInventoryDigest(ids []string) string {
	parts := make([][]byte, len(ids))
	for index := range ids {
		parts[index] = []byte(ids[index])
	}
	return domainHash(retentionUnsealedDigestDomain, parts...)
}

func retentionPlanByteLimit(limits Limits) int64 {
	if limits.MaxManifestBytes < MaxRetentionPlanBytesV1 {
		return limits.MaxManifestBytes
	}
	return MaxRetentionPlanBytesV1
}
