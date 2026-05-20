package main

import (
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"

	"github.com/Martin-Hausleitner/beeper-matrix-proxy/connector"
)

var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

func main() {
	connector := &connector.MyConnector{}

	m := mxmain.BridgeMain{
		// Keep the internal mxmain name stable for existing installations:
		// mautrix uses it as the database owner key ("megabridge/<name>").
		// The public bridge/network identity is exposed by the connector.
		Name:        "minibridge",
		Description: "A Matrix-to-Beeper bridgev2 proxy for private Matrix homeservers.",
		Version:     "0.1.0",
		URL:         "https://github.com/Martin-Hausleitner/beeper-matrix-proxy",
		Connector:   connector,
	}

	m.InitVersion(Tag, Commit, BuildTime)
	m.Run()
}
