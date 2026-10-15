package validate

import (
	"fmt"

	"opm/agent/internal/model"
)

// checkBackupPolicy is TS checkBackupPolicy (§21 #112 SiteVolume.backupPolicy: absent = the daily site snapshot).
func checkBackupPolicy(v *model.SiteVolume, vp string, is *Issues) {
	p := v.BackupPolicy
	if p == nil {
		return
	}
	at := vp + ".backupPolicy"
	if _, ok := model.VolumeBackupSchedules[p.Schedule]; !ok {
		is.err(at+".schedule", "V_ENUM", "schedule must be off, hourly, 6h, daily or weekly")
	}
	if p.Schedule != "off" && (p.Keep < 1 || p.Keep > 100) {
		is.err(at+".keep", "V_RANGE", fmt.Sprintf("keep must be 1 … 100 (got %d)", p.Keep))
	}
	if p.Schedule != "off" && !v.Backup {
		is.Warn(at, "V_VOLUME_BACKUP_OFF", fmt.Sprintf("volume %s has backup off: its automatic backups do not run", v.VolumeSlug))
	}
}
