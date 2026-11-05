package update

import (
	"errors"
	"fmt"
)

func ChannelVersion(channel, version string) (string, error) {
	switch channel {
	case "stable":
		return version, nil
	case ChannelDev:
		if version != "" {
			return "", fmt.Errorf("do not combine --channel %s with --version", channel)
		}
		return channel, nil
	default:
		return "", errors.New("update channel must be stable or dev")
	}
}
