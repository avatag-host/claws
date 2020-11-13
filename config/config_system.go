package config

import (
	"context"
	"fmt"
	"github.com/apex/log"
	"github.com/pkg/errors"
	"html/template"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"time"
)

// Defines basic system configuration settings.
type SystemConfiguration struct {
	// The root directory where all of the pterodactyl data is stored at.
	RootDirectory string `default:"/var/lib/panther" yaml:"root_directory"`

	// Directory where logs for server installations and other wings events are logged.
	LogDirectory string `default:"/var/log/panther" yaml:"log_directory"`

	// Directory where the server data is stored at.
	Data string `default:"/var/lib/panther/volumes" yaml:"data"`

	// Directory where server archives for transferring will be stored.
	ArchiveDirectory string `default:"/var/lib/panther/archives" yaml:"archive_directory"`

	// Directory where local backups will be stored on the machine.
	BackupDirectory string `default:"/var/lib/panther/backups" yaml:"backup_directory"`

	// The user that should own all of the server files, and be used for containers.
	Username string `default:"panther" yaml:"username"`

	// The timezone for this Wings instance. This is detected by Wings automatically if possible,
	// and falls back to UTC if not able to be detected. If you need to set this manually, that
	// can also be done.
	//
	// This timezone value is passed into all containers created by Wings.
	Timezone string `yaml:"timezone"`

	// Definitions for the user that gets created to ensure that we can quickly access
	// this information without constantly having to do a system lookup.
	User struct {
		Uid int
		Gid int
	}

	// The amount of time in seconds that can elapse before a server's disk space calculation is
	// considered stale and a re-check should occur. DANGER: setting this value too low can seriously
	// impact system performance and cause massive I/O bottlenecks and high CPU usage for the Wings
	// process.
	DiskCheckInterval int64 `default:"150" yaml:"disk_check_interval"`

	// Determines if Wings should detect a server that stops with a normal exit code of
	// "0" as being crashed if the process stopped without any Wings interaction. E.g.
	// the user did not press the stop button, but the process stopped cleanly.
	DetectCleanExitAsCrash bool `default:"true" yaml:"detect_clean_exit_as_crash"`

	// If set to true, file permissions for a server will be checked when the process is
	// booted. This can cause boot delays if the server has a large amount of files. In most
	// cases disabling this should not have any major impact unless external processes are
	// frequently modifying a servers' files.
	CheckPermissionsOnBoot bool `default:"true" yaml:"check_permissions_on_boot"`

	// If set to false Wings will not attempt to write a log rotate configuration to the disk
	// when it boots and one is not detected.
	EnableLogRotate bool `default:"true" yaml:"enable_log_rotate"`
}

// Ensures that all of the system directories exist on the system. These directories are
// created so that only the owner can read the data, and no other users.
func (sc *SystemConfiguration) ConfigureDirectories() error {
	log.WithField("path", sc.RootDirectory).Debug("ensuring root data directory exists")
	if err := os.MkdirAll(sc.RootDirectory, 0700); err != nil {
		return err
	}

	// There are a non-trivial number of users out there whose data directories are actually a
	// symlink to another location on the disk. If we do not resolve that final destination at this
	// point things will appear to work, but endless errors will be encountered when we try to
	// verify accessed paths since they will all end up resolving outside the expected data directory.
	//
	// For the sake of automating away as much of this as possible, see if the data directory is a
	// symlink, and if so resolve to its final real path, and then update the configuration to use
	// that.
	if d, err := filepath.EvalSymlinks(sc.Data); err != nil {
		if !os.IsNotExist(err) {
			return errors.WithStack(err)
		}
	} else if d != sc.Data {
		sc.Data = d
	}

	log.WithField("path", sc.Data).Debug("ensuring server data directory exists")
	if err := os.MkdirAll(sc.Data, 0700); err != nil {
		return err
	}

	log.WithField("path", sc.ArchiveDirectory).Debug("ensuring archive data directory exists")
	if err := os.MkdirAll(sc.ArchiveDirectory, 0700); err != nil {
		return err
	}

	log.WithField("path", sc.BackupDirectory).Debug("ensuring backup data directory exists")
	if err := os.MkdirAll(sc.BackupDirectory, 0700); err != nil {
		return err
	}

	return nil
}

// Writes a logrotate file for wings to the system logrotate configuration directory if one
// exists and a logrotate file is not found. This allows us to basically automate away the log
// rotation for most installs, but also enable users to make modifications on their own.
func (sc *SystemConfiguration) EnableLogRotation() error {
	// Do nothing if not enabled.
	if sc.EnableLogRotate == false {
		log.Info("skipping log rotate configuration, disabled in wings config file")

		return nil
	}

	if st, err := os.Stat("/etc/logrotate.d"); err != nil && !os.IsNotExist(err) {
		return errors.WithStack(err)
	} else if (err != nil && os.IsNotExist(err)) || !st.IsDir() {
		return nil
	}

	if _, err := os.Stat("/etc/logrotate.d/claws"); err != nil && !os.IsNotExist(err) {
		return errors.WithStack(err)
	} else if err == nil {
		return nil
	}

	log.Info("no log rotation configuration found, system is configured to support it, adding file now")
	// If we've gotten to this point it means the logrotate directory exists on the system
	// but there is not a file for wings already. In that case, let us write a new file to
	// it so files can be rotated easily.
	f, err := os.Create("/etc/logrotate.d/claws")
	if err != nil {
		return errors.WithStack(err)
	}
	defer f.Close()

	t, err := template.New("logrotate").Parse(`
{{.LogDirectory}}/wings.log {
    size 10M
    compress
    delaycompress
    dateext
    maxage 7
    missingok
    notifempty
    create 0640 {{.User.Uid}} {{.User.Gid}}
    postrotate
        killall -SIGHUP wings
    endscript
}`)

	if err != nil {
		return errors.WithStack(err)
	}

	return errors.Wrap(t.Execute(f, sc), "failed to write logrotate file to disk")
}

// Returns the location of the JSON file that tracks server states.
func (sc *SystemConfiguration) GetStatesPath() string {
	return path.Join(sc.RootDirectory, "states.json")
}

// Returns the location of the JSON file that tracks server states.
func (sc *SystemConfiguration) GetInstallLogPath() string {
	return path.Join(sc.LogDirectory, "install/")
}

// Configures the timezone data for the configuration if it is currently missing. If
// a value has been set, this functionality will only run to validate that the timezone
// being used is valid.
func (sc *SystemConfiguration) ConfigureTimezone() error {
	if sc.Timezone == "" {
		if b, err := ioutil.ReadFile("/etc/timezone"); err != nil {
			if !os.IsNotExist(err) {
				return errors.Wrap(err, "failed to open /etc/timezone for automatic server timezone calibration")
			}

			ctx, _ := context.WithTimeout(context.Background(), time.Second * 5)
			// Okay, file isn't found on this OS, we will try using timedatectl to handle this. If this
			// command fails, exit, but if it returns a value use that. If no value is returned we will
			// fall through to UTC to get Wings booted at least.
			out, err := exec.CommandContext(ctx, "timedatectl").Output()
			if err != nil {
				log.WithField("error", err).Warn("failed to execute \"timedatectl\" to determine system timezone, falling back to UTC")

				sc.Timezone = "UTC"
				return nil
			}

			r := regexp.MustCompile(`Time zone: ([\w/]+)`)
			matches := r.FindSubmatch(out)
			if len(matches) != 2 || string(matches[1]) == "" {
				log.Warn("failed to parse timezone from \"timedatectl\" output, falling back to UTC")

				sc.Timezone = "UTC"
				return nil
			}

			sc.Timezone = string(matches[1])
		} else {
			sc.Timezone = string(b)
		}
	}

	sc.Timezone = regexp.MustCompile(`(?i)[^a-z_/]+`).ReplaceAllString(sc.Timezone, "")

	_, err := time.LoadLocation(sc.Timezone)

	return errors.Wrap(err, fmt.Sprintf("the supplied timezone %s is invalid", sc.Timezone))
}