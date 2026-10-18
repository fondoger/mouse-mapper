package desktop

import (
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
)

const updateIntervalDays = 7

type UpdateState struct {
	Enabled            bool      `json:"enabled"`
	AutomaticChecks    bool      `json:"automaticChecks"`
	AutomaticDownloads bool      `json:"automaticDownloads"`
	LastCheck          time.Time `json:"lastCheck"`
	IntervalDays       int       `json:"intervalDays"`
}

type updateService interface {
	state() UpdateState
	check()
	setChecks(bool)
	setDownloads(bool)
}

type nativeUpdater struct{}

func (nativeUpdater) state() UpdateState {
	return UpdateState{
		Enabled:            mygo.Updater.Enabled(),
		AutomaticChecks:    updater.AutomaticChecks(),
		AutomaticDownloads: updater.AutomaticDownloads(),
		LastCheck:          updater.LastCheck(),
		IntervalDays:       updateIntervalDays,
	}
}

func (nativeUpdater) check()               { updater.CheckForUpdates() }
func (nativeUpdater) setChecks(on bool)    { updater.SetAutomaticChecks(on) }
func (nativeUpdater) setDownloads(on bool) { updater.SetAutomaticDownloads(on) }

// CheckForUpdates opens MyGo's native update window. The official plugin owns
// signature verification, download progress, installation and safe relaunch.
func (m *Mapper) CheckForUpdates() {
	if m.updates != nil {
		m.updates.check()
	}
}

// SetAutomaticChecks changes the official updater's persisted preference.
func (m *Mapper) SetAutomaticChecks(on bool) State {
	if m.updates != nil {
		m.updates.setChecks(on)
		m.publish()
	}
	return m.GetState()
}

// SetAutomaticDownloads opts in to downloading and staging signed updates
// without another prompt. It is off by default; new code runs on next launch.
func (m *Mapper) SetAutomaticDownloads(on bool) State {
	if m.updates != nil {
		m.updates.setDownloads(on)
		m.publish()
	}
	return m.GetState()
}
