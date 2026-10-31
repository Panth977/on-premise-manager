package routes

// PruneVolumeContent is a STUB until the port of routes/volume-content.ts is merged: that file defines the real
// pruneVolumeContent. The merge then fails to compile with a duplicate symbol, on purpose: delete this file (and match the
// result type in daily.go if it differs).
func PruneVolumeContent() (int, error) { return 0, nil }
