package desktop

import (
	"fmt"

	"github.com/gen2brain/beeep"

	"aliang.one/nursorgate/common/logger"
)

// Notify sends a desktop notification with the given title and message.
// Errors are logged but never propagated — notifications are best-effort.
func Notify(title, message string) {
	if err := beeep.Notify(title, message, ""); err != nil {
		logger.Warn(fmt.Sprintf("Desktop notification failed: %v", err))
	}
}
