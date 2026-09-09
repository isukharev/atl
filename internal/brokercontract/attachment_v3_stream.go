package brokercontract

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"

	"github.com/isukharev/atl/internal/domain"
)

// ValidateAttachmentTerminalLineForReleaseV3 binds an emitted terminal line
// to the exact authorized release facts and surrounding stream state.
func ValidateAttachmentTerminalLineForReleaseV3(value domain.BrokerAttachmentTerminalLineV3, facts domain.BrokerAttachmentReleaseFactsV3, declaredSize int64, streamID, priorReleaseSHA256, releaseDecisionSHA256 string) error {
	if !validAttachmentTerminalLineV3(value) || !validAttachmentReleaseFactsV3(facts) || declaredSize < 0 || declaredSize > MaxAttachmentNativeBodyBytesV3 ||
		value.StreamID != streamID || value.DeclaredSize != declaredSize || value.PriorReleaseSHA256 != priorReleaseSHA256 || value.ReleaseDecisionSHA256 != releaseDecisionSHA256 {
		return reject(domain.BrokerReasonMalformed)
	}
	var terminal domain.BrokerAttachmentReleaseTerminalFactsV3
	switch facts.Kind {
	case domain.BrokerAttachmentReleaseData:
		if facts.Data == nil || facts.Data.Terminal == nil {
			return reject(domain.BrokerReasonMalformed)
		}
		terminal = *facts.Data.Terminal
		if terminal.WholeSHA256 != facts.Data.CumulativeSHA256 {
			return reject(domain.BrokerReasonMalformed)
		}
	case domain.BrokerAttachmentReleaseTerminal:
		if facts.Terminal == nil {
			return reject(domain.BrokerReasonMalformed)
		}
		terminal = *facts.Terminal
	default:
		return reject(domain.BrokerReasonMalformed)
	}
	if value.ChunkCount != terminal.ChunkCount || value.TotalBytes != terminal.TotalBytes || value.WholeSHA256 != terminal.WholeSHA256 || !value.EOFProven || !value.Complete {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func validAttachmentDataLineV3(value domain.BrokerAttachmentDataLineV3) bool {
	decoded, err := base64.StdEncoding.Strict().DecodeString(value.PayloadBase64)
	digest := sha256.Sum256(decoded)
	return value.SchemaVersion == ExecutionSchemaVersionV3 && value.FrameVersion == AttachmentFrameVersionV3 && value.Kind == domain.BrokerAttachmentReleaseData && validIdentifier(value.StreamID) &&
		value.Index >= 0 && value.Index < MaxAttachmentDataFramesV3 && value.Offset == int64(value.Index)*MaxAttachmentDecodedFrameBytesV3 && value.DecodedBytes > 0 && value.DecodedBytes <= MaxAttachmentDecodedFrameBytesV3 && int64(len(decoded)) == value.DecodedBytes &&
		err == nil && base64.StdEncoding.EncodeToString(decoded) == value.PayloadBase64 && hex.EncodeToString(digest[:]) == value.PayloadSHA256 && value.CumulativeBytes == value.Offset+value.DecodedBytes &&
		value.CumulativeBytes <= MaxAttachmentNativeBodyBytesV3 && validDigest(value.CumulativeSHA256) && validDigest(value.ResourceSHA256) && validDigest(value.PriorReleaseSHA256) && validDigest(value.ReleaseDecisionSHA256)
}

func validAttachmentTerminalLineV3(value domain.BrokerAttachmentTerminalLineV3) bool {
	facts := domain.BrokerAttachmentReleaseTerminalFactsV3{ChunkCount: value.ChunkCount, TotalBytes: value.TotalBytes, WholeSHA256: value.WholeSHA256, EOFProven: value.EOFProven, Complete: value.Complete}
	return value.SchemaVersion == ExecutionSchemaVersionV3 && value.FrameVersion == AttachmentFrameVersionV3 && value.Kind == domain.BrokerAttachmentReleaseTerminal && validIdentifier(value.StreamID) &&
		value.TotalBytes == value.DeclaredSize && validAttachmentTerminalFactsV3(facts) && validDigest(value.PriorReleaseSHA256) && validDigest(value.ReleaseDecisionSHA256)
}
