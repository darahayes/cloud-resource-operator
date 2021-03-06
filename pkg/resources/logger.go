package resources

import "github.com/sirupsen/logrus"

const (
	LoggingKeyAction = "action"
)

func NewActionLogger(logger *logrus.Entry, action string) *logrus.Entry {
	return logger.WithField(LoggingKeyAction, action)
}
