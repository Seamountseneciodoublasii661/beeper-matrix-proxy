package connector

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/configupgrade"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/id"
)

// Ensure MyConnector implements NetworkConnector.
var _ bridgev2.NetworkConnector = (*MyConnector)(nil)

// MyConnector implements the NetworkConnector interface.
type MyConnector struct {
	log    zerolog.Logger
	bridge *bridgev2.Bridge
}

// NewMyConnector creates a new instance of MyConnector.
func NewMyConnector(log zerolog.Logger) *MyConnector {
	return &MyConnector{
		log: log.With().Str("component", "network-connector").Logger(),
	}
}

// Init initializes the connector with the bridge instance.
func (c *MyConnector) Init(br *bridgev2.Bridge) {
	c.bridge = br
	c.log = c.bridge.Log
	c.log.Info().Msg("MyConnector Init called")
}

// GetName implements bridgev2.NetworkConnector.
func (c *MyConnector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:          "Beeper Matrix Proxy",
		NetworkURL:           localHomeserverURL(),
		NetworkIcon:          "",
		NetworkID:            "beeper-matrix-proxy",
		BeeperBridgeType:     "beeper-matrix-proxy",
		DefaultPort:          29320,
		DefaultCommandPrefix: "!matrixproxy",
	}
}

// GetNetworkID implements bridgev2.NetworkConnector.
func (c *MyConnector) GetNetworkID() string {
	return c.GetName().NetworkID
}

// GetCapabilities implements bridgev2.NetworkConnector.
func (c *MyConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{}
}

// GetDBMetaTypes implements bridgev2.NetworkConnector.
func (c *MyConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		Portal: func() any {
			return &PortalMetadata{}
		},
		Ghost: func() any {
			return &GhostMetadata{}
		},
		Reaction: func() any {
			return &ReactionMetadata{}
		},
		UserLogin: func() any {
			return &LoginMetadata{}
		},
	}
}

// GetLoginFlows implements bridgev2.NetworkConnector.
func (c *MyConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{{
		ID:          LoginFlowIDUsernamePassword,
		Name:        "Username & Password",
		Description: "Log in to the remote Matrix homeserver.",
	}}
}

// CreateLogin implements bridgev2.NetworkConnector.
func (c *MyConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	if flowID != LoginFlowIDUsernamePassword {
		return nil, fmt.Errorf("unsupported login flow ID: %s", flowID)
	}
	return &SimpleLogin{
		User: user,
		Main: c,
		Log:  user.Log.With().Str("action", "login").Str("flow", flowID).Logger(),
	}, nil
}

// GetConfig implements bridgev2.NetworkConnector.
func (c *MyConnector) GetConfig() (string, any, configupgrade.Upgrader) {
	return "beeper-matrix-proxy.yaml", nil, nil
}

// GetBridgeInfoVersion implements bridgev2.NetworkConnector.
func (c *MyConnector) GetBridgeInfoVersion() (int, int) {
	return 1, 5
}

// Start implements bridgev2.NetworkConnector.
func (c *MyConnector) Start(ctx context.Context) error {
	c.log.Info().Msg("MyConnector Start called")
	return nil
}

// Stop implements bridgev2.NetworkConnector.
func (c *MyConnector) Stop(ctx context.Context) error {
	c.log.Info().Msg("MyConnector Stop called")
	return nil
}

// LoadUserLogin implements bridgev2.NetworkConnector.
func (c *MyConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	c.log.Info().
		Str("user_id", string(login.ID)).
		Str("remote_name", login.RemoteName).
		Str("mxid", string(login.User.MXID)).
		Msg("LoadUserLogin called")

	client := &MyNetworkClient{
		log:        c.log.With().Str("user_id", string(login.ID)).Logger(),
		bridge:     c.bridge,
		login:      login,
		connector:  c,
		sentEvents: make(map[id.EventID]struct{}),
	}
	if meta, ok := login.Metadata.(*LoginMetadata); ok {
		cli, err := newLocalMatrixClient(meta.UserID, meta.AccessToken)
		if err != nil {
			return err
		}
		cli.DeviceID = id.DeviceID(meta.DeviceID)
		client.mx = cli
		client.loggedIn = meta.AccessToken != ""
	}

	login.Client = client

	c.log.Info().
		Str("user_id", string(login.ID)).
		Str("remote_name", login.RemoteName).
		Interface("client_type", client).
		Msg("Created and stored MyNetworkClient")

	return nil
}

func localHomeserverURL() string {
	if value := os.Getenv("LOCAL_MATRIX_HS"); value != "" {
		return value
	}
	return "https://matrix.example.com"
}

func newLocalMatrixClient(userID, accessToken string) (*mautrix.Client, error) {
	cli, err := mautrix.NewClient(localHomeserverURL(), id.UserID(userID), accessToken)
	if err != nil {
		return nil, err
	}
	if os.Getenv("LOCAL_MATRIX_INSECURE_TLS") != "0" {
		cli.Client = &http.Client{
			Timeout: 180 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
	}
	return cli, nil
}

func (c *MyConnector) resyncRoom(ctx context.Context, login *bridgev2.UserLogin, roomID id.RoomID, info *bridgev2.ChatInfo) {
	c.bridge.QueueRemoteEvent(login, &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    networkid.PortalKey{ID: networkid.PortalID(roomID), Receiver: login.ID},
			CreatePortal: true,
			Timestamp:    time.Now(),
		},
		ChatInfo: info,
	})
}
