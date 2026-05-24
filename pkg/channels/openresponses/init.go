package openresponses

import (
	"fmt"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func init() {
	channels.RegisterFactory(config.ChannelOpenResponses, func(channelName, channelType string, cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
		bc := cfg.Channels[channelName]
		if bc == nil {
			return nil, fmt.Errorf("channel %q: config not found", channelName)
		}
		decoded, err := bc.GetDecoded()
		if err != nil {
			return nil, fmt.Errorf("channel %q: failed to decode settings: %w", channelName, err)
		}
		settings, ok := decoded.(*config.OpenResponsesSettings)
		if !ok {
			return nil, fmt.Errorf("channel %q: expected OpenResponsesSettings, got %T", channelName, decoded)
		}
		ch, err := NewOpenResponsesChannel(bc, settings, b, cfg.WorkspacePath())
		if err != nil {
			return nil, err
		}
		if bc.Name() != config.ChannelOpenResponses {
			ch.SetName(bc.Name())
		}
		return ch, nil
	})
}
