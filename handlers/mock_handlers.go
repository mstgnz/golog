package handlers

import (
	"github.com/mstgnz/golog/models"
)

// Mock functions for testing
var (
	// GetLogs is a mock function for database.GetLogs
	GetLogs func(filter models.LogFilter) ([]models.Log, error)

	// InsertLog is a mock function for database.InsertLog
	InsertLog func(logEntry models.Log) (int, error)
)
