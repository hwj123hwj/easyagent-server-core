// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ProposalFingerprint binds a normalized call and host-read intent to the exact
// principal, assistant version and operation. It is not proof of authorization
// or approval. Callers validate input with the registered Schema first; hosts
// persist/authenticate proposals and transactionally consume confirmations.
func ProposalFingerprint(ec ExecutionContext, c Call, intent Intent) string {
	b, _ := json.Marshal(struct {
		Principal        Principal
		AssistantID      string
		AssistantVersion string
		OperationID      string
		Call             Call
		Intent           Intent
	}{ec.Principal, ec.AssistantID, ec.AssistantVersion, ec.OperationID, c, intent})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
