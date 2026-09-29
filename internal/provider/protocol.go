// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"os"
)

// ProtocolEnvVarName selects the plugin protocol the provider is served over.
//
// tfcoremock has always served protocol 6. Many real providers serve only
// protocol 5 (anything muxing an SDKv2 server, among them several that ship
// actions), so a client's protocol-5 transport needs a provider to be tested
// against; this lets one corpus run over either. It is an environment variable
// because the choice is made before the provider exists, at Serve.
const ProtocolEnvVarName = "TFCOREMOCK_PROTOCOL"

// ProtocolFromEnv reads the protocol major version to serve. Unset or "6" is
// 6, "5" is 5, and anything else is an error: a typo must never quietly serve
// the default, or a run meant for protocol 5 would pass on protocol 6.
func ProtocolFromEnv() (int, error) {
	switch raw := os.Getenv(ProtocolEnvVarName); raw {
	case "", "6":
		return 6, nil
	case "5":
		return 5, nil
	default:
		return 0, fmt.Errorf("%s must be 5 or 6, got %q", ProtocolEnvVarName, raw)
	}
}
