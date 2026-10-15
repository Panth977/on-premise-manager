package validate

import (
	"strings"
	"testing"

	"opm/agent/internal/model"
)

func TestCheckBackupPolicy(t *testing.T) {
	codes := func(v model.SiteVolume) string {
		var is Issues
		checkBackupPolicy(&v, "sites[a].config.volumes[x]", &is)
		var out []string
		for _, i := range is.List {
			out = append(out, i.Code+":"+i.Severity)
		}
		return strings.Join(out, ",")
	}
	ok := model.SiteVolume{VolumeSlug: "x", Backup: true}
	if c := codes(ok); c != "" {
		t.Fatal(c)
	}
	ok.BackupPolicy = &model.VolumeBackupPolicy{Schedule: "hourly", Keep: 24}
	if c := codes(ok); c != "" {
		t.Fatal(c)
	}
	bad := model.SiteVolume{VolumeSlug: "x", Backup: true, BackupPolicy: &model.VolumeBackupPolicy{Schedule: "minutely", Keep: 0}}
	if c := codes(bad); c != "V_ENUM:error,V_RANGE:error" {
		t.Fatal(c)
	}
	off := model.SiteVolume{VolumeSlug: "x", Backup: true, BackupPolicy: &model.VolumeBackupPolicy{Schedule: "off", Keep: 0}}
	if c := codes(off); c != "" {
		t.Fatal(c)
	}
	noBackup := model.SiteVolume{VolumeSlug: "x", Backup: false, BackupPolicy: &model.VolumeBackupPolicy{Schedule: "daily", Keep: 7}}
	if c := codes(noBackup); c != "V_VOLUME_BACKUP_OFF:warning" {
		t.Fatal(c)
	}
	if p := noBackup.BackupPolicyOf(); p.Schedule != "off" {
		t.Fatal(p)
	}
	if p := (model.SiteVolume{Backup: true}).BackupPolicyOf(); p != model.DefaultVolumeBackupPolicy {
		t.Fatal(p)
	}
}
