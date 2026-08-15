package logger

// HttpDebug logs debug message to HTTP logger
func HttpDebug(v ...interface{}) {
	GetMainLogger().Debug(v...)
}

// HttpInfo logs info message to HTTP logger
func HttpInfo(v ...interface{}) {
	GetMainLogger().Info(v...)
}

// HttpWarn logs warn message to HTTP logger
func HttpWarn(v ...interface{}) {
	GetMainLogger().Warn(v...)
}

// HttpError logs error message to HTTP logger
func HttpError(v ...interface{}) {
	GetMainLogger().Error(v...)
}

// HttpTrace logs trace message to the unified logger.
func HttpTrace(v ...interface{}) {
	GetMainLogger().Trace(v...)
}
