package cmd

import (
	"github.com/avatag-host/claws/config"
	"github.com/avatag-host/claws/utils"
	"os"
	"path/filepath"
	"runtime"
)

// We've gone through a couple of iterations of where the configuration is stored. This
// helpful little function will look through the three areas it might have ended up, and
// return it.
//
// We only run this if the configuration flag for the instance is not actually passed in
// via the command line. Once found, the configuration is moved into the expected default
// location. Only errors are returned from this function, you can safely assume that after
// running this the configuration can be found in the correct default location.
func RelocateConfiguration() error {
	var match string
	var check []string;
	var currentDirectory string;
	dir, err := os.Getwd()

	if err != nil {

	}
	currentDirectory = dir;
	if runtime.GOOS == "windows" {
		check = []string{
			currentDirectory + "/config.yml",
			config.DefaultLocationWindows,
		}
	} else {
		check = []string{
			currentDirectory + "/config.yml",
			config.DefaultLocationLinux,
		}
	}


	// Loop over all of the configuration paths, and return which one we found, if
	// any.
	for _, p := range check {
		if s, err := os.Stat(p); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		} else if !s.IsDir() {
			match = p
			break
		}
	}

	// Just return a generic not exist error at this point if we didn't have a match, this
	// will allow the caller to handle displaying a more friendly error to the user. If we
	// did match in the default location, go ahead and return successfully.
	if match == "" {
		return os.ErrNotExist
	} else if match == config.DefaultLocationLinux || match == config.DefaultLocationWindows{
		return nil
	}

	// The rest of this function simply creates the new default location and moves the
	// old configuration file over to the new location, then sets the permissions on the
	// file correctly so that only the user running this process can read it.

	if runtime.GOOS == "windows" {
		p, _ := filepath.Split(config.DefaultLocationWindows)
		if err := os.MkdirAll(p, 0755); err != nil {
			return err
		}

		if err := utils.MoveFile(match, config.DefaultLocationWindows); err != nil {
			return err
		}

		return os.Chmod(config.DefaultLocationWindows, 0600)
	} else {
		p, _ := filepath.Split(config.DefaultLocationLinux)
		if err := os.MkdirAll(p, 0755); err != nil {
			return err
		}


		if err := utils.MoveFile(match, config.DefaultLocationLinux); err != nil {
			return err
		}

		return os.Chmod(config.DefaultLocationLinux, 0600)
	}
}
