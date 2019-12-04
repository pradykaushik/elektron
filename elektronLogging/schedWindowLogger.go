package elektronLogging

import (
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

type SchedWindowLogger struct {
	LoggerImpl
}

func NewSchedWindowLogger(b *baseLogData, logType int, prefix string, logger *log.Logger) *SchedWindowLogger {
	sLog := &SchedWindowLogger{}
	sLog.logType = logType
	sLog.CreateLogFile(prefix)
	sLog.next = nil
	sLog.baseLogData = b
	sLog.logger = logger
	return sLog
}

func (sLog SchedWindowLogger) Log(logType int, level log.Level, message string) {
	if sLog.logType == logType {
		if config.SchedWindowConfig.Enabled {
			if sLog.allowOnConsole {
				sLog.logger.SetOutput(os.Stdout)
				sLog.logger.WithFields(sLog.data).Log(level, message)
			}

			sLog.logger.SetOutput(sLog.logFile)
			sLog.logger.WithFields(sLog.data).Log(level, message)
		}
	}
	// Forwarding to next logger
	if sLog.next != nil {
		sLog.next.Log(logType, level, message)
	} else {
		// Clearing the fields.
		sLog.resetFields()
	}
}

func (sLog SchedWindowLogger) Logf(logType int, level log.Level, msgFmtString string, args ...interface{}) {
	if sLog.logType == logType {
		if config.SchedWindowConfig.Enabled {
			if sLog.allowOnConsole {
				sLog.logger.SetOutput(os.Stdout)
				sLog.logger.WithFields(sLog.data).Logf(level, msgFmtString, args...)
			}

			sLog.logger.SetOutput(sLog.logFile)
			sLog.logger.WithFields(sLog.data).Logf(level, msgFmtString, args...)
		}
	}
	if sLog.next != nil {
		sLog.next.Logf(logType, level, msgFmtString, args...)
	} else {
		// Clearing the fields.
		sLog.resetFields()
	}
}

func (sLog *SchedWindowLogger) CreateLogFile(prefix string) {
	if config.SchedWindowConfig.Enabled {
		filename := strings.Join([]string{prefix, config.SchedWindowConfig.FilenameExtension}, "")
		dirName := logDir.getDirName()
		if dirName != "" {
			if logFile, err := os.Create(filepath.Join(dirName, filename)); err != nil {
				log.Fatal("Unable to create logFile: ", err)
			} else {
				sLog.logFile = logFile
				sLog.allowOnConsole = config.SchedWindowConfig.AllowOnConsole
			}
		}
	}
}
