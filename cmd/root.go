package cmd

import (
	"crypto/tls"
	"fmt"
	"github.com/NYTimes/logrotate"
	"github.com/apex/log"
	"github.com/apex/log/handlers/multi"
	"github.com/avatag-host/claws/loggers/cli"
	"github.com/docker/docker/client"
	"github.com/gammazero/workerpool"
	"github.com/mitchellh/colorstring"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"

	"github.com/avatag-host/claws/config"
	"github.com/avatag-host/claws/environment"
	"github.com/avatag-host/claws/router"
	"github.com/avatag-host/claws/server"
	"github.com/avatag-host/claws/system"
	"github.com/pkg/errors"
	"github.com/pkg/profile"
	"github.com/spf13/cobra"
)

var configPath string;

var debug = false
var shouldRunProfiler = false
var useAutomaticTls = false
var tlsHostname = ""
var showVersion = false

var root = &cobra.Command{
	Use:   "claws",
	Short: "The claws of the panther game management panel",
	Long:  ``,
	PreRun: func(cmd *cobra.Command, args []string) {
		if useAutomaticTls && len(tlsHostname) == 0 {
			fmt.Println("A TLS hostname must be provided when running wings with automatic TLS, e.g.:\n\n    ./wings --auto-tls --tls-hostname my.example.com")
			os.Exit(1)
		}
	},
	Run: rootCmdRun,
}

func init() {
	if runtime.GOOS == "windows" {
		configPath = config.DefaultLocationWindows
		root.PersistentFlags().StringVar(&configPath, "config", config.DefaultLocationWindows, "set the location for the configuration file")
	} else {
		configPath = config.DefaultLocationLinux;
		root.PersistentFlags().StringVar(&configPath, "config", config.DefaultLocationLinux, "set the location for the configuration file")
	}
	root.PersistentFlags().BoolVar(&showVersion, "version", false, "show the version and exit")
	root.PersistentFlags().BoolVar(&debug, "debug", false, "pass in order to run wings in debug mode")
	root.PersistentFlags().BoolVar(&shouldRunProfiler, "profile", false, "pass in order to profile wings")
	root.PersistentFlags().BoolVar(&useAutomaticTls, "auto-tls", false, "pass in order to have wings generate and manage it's own SSL certificates using Let's Encrypt")
	root.PersistentFlags().StringVar(&tlsHostname, "tls-hostname", "", "required with --auto-tls, the FQDN for the generated SSL certificate")

	root.AddCommand(configureCmd)
	root.AddCommand(diagnosticsCmd)
}

// Get the configuration path based on the arguments provided.
func readConfiguration() (*config.Configuration, error) {
	if s, err := os.Stat(configPath); err != nil {
		return nil, errors.WithStack(err)
	} else if s.IsDir() {
		return nil, errors.New("cannot use directory as configuration file path")
	}

	return config.ReadConfiguration(configPath)
}

func rootCmdRun(*cobra.Command, []string) {
	if showVersion {
		fmt.Println(system.Version)
		os.Exit(0)
	}

	if shouldRunProfiler {
		defer profile.Start().Stop()
	}

	// Only attempt configuration file relocation if a custom location has not
	// been specified in the command startup.
	if configPath == config.DefaultLocationLinux || configPath == config.DefaultLocationWindows {
		if err := RelocateConfiguration(); err != nil {
			if os.IsNotExist(err) {
				exitWithConfigurationNotice()
			}

			panic(err)
		}
	}
	c, err := readConfiguration()
	if err != nil {
		panic(err)
	}

	if debug {
		c.Debug = true
	}

	printLogo()
	if err := configureLogging(c.System.LogDirectory, c.Debug); err != nil {
		panic(err)
	}

	log.WithField("path", c.GetPath()).Info("loading configuration from path")
	if c.Debug {
		log.Debug("running in debug mode")
		log.Warn("certificate checking is disabled")

		http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	config.Set(c)
	config.SetDebugViaFlag(debug)

	if err := c.System.ConfigureTimezone(); err != nil {
		log.WithField("error", err).Fatal("failed to detect system timezone or use supplied configuration value")
		return
	}

	log.WithField("timezone", c.System.Timezone).Info("configured wings with system timezone")

	if err := c.System.ConfigureDirectories(); err != nil {
		log.WithField("error", err).Fatal("failed to configure system directories for panther")
		return
	}

	if err := c.System.EnableLogRotation(); err != nil {
		log.WithField("error", err).Fatal("failed to configure log rotation on the system")
		return
	}

	log.WithField("username", c.System.Username).Info("checking for panther system user")
	if su, err := c.EnsurePterodactylUser(); err != nil {
		log.WithField("error", err).Fatal("failed to create panther system user")
		return
	} else {
		log.WithFields(log.Fields{
			"username": su.Username,
			"uid":      su.Uid,
			"gid":      su.Gid,
		}).Info("configured system user successfully")
	}

	if err := server.LoadDirectory(); err != nil {
		log.WithField("error", err).Fatal("failed to load server configurations")
		return
	}

	if err := environment.ConfigureDocker(&c.Docker); err != nil {
		log.WithField("error", err).Fatal("failed to configure docker environment")
		return
	}

	if err := c.WriteToDisk(); err != nil {
		log.WithField("error", err).Error("failed to save configuration to disk")
	}

	// Just for some nice log output.
	for _, s := range server.GetServers().All() {
		log.WithField("server", s.Id()).Info("loaded configuration for server")
	}

	states, err := server.CachedServerStates()
	if err != nil {
		log.WithField("error", errors.WithStack(err)).Error("failed to retrieve locally cached server states from disk, assuming all servers in offline state")
	}

	// Create a new workerpool that limits us to 4 servers being bootstrapped at a time
	// on Wings. This allows us to ensure the environment exists, write configurations,
	// and reboot processes without causing a slow-down due to sequential booting.
	pool := workerpool.New(4)

	for _, serv := range server.GetServers().All() {
		s := serv

		pool.Submit(func() {
			s.Log().Info("configuring server environment and restoring to previous state")

			var st string
			if state, exists := states[s.Id()]; exists {
				st = state
			}

			r, err := s.Environment.IsRunning()
			// We ignore missing containers because we don't want to actually block booting of wings at this
			// point. If we didn't do this and you pruned all of the images and then started wings you could
			// end up waiting a long period of time for all of the images to be re-pulled on Wings boot rather
			// than when the server itself is started.
			if err != nil && !client.IsErrNotFound(err) {
				s.Log().WithField("error", err).Error("error checking server environment status")
			}

			// Check if the server was previously running. If so, attempt to start the server now so that Wings
			// can pick up where it left off. If the environment does not exist at all, just create it and then allow
			// the normal flow to execute.
			//
			// This does mean that booting wings after a catastrophic machine crash and wiping out the Docker images
			// as a result will result in a slow boot.
			if !r && (st == environment.ProcessRunningState || st == environment.ProcessStartingState) {
				if err := s.HandlePowerAction(server.PowerActionStart); err != nil {
					s.Log().WithField("error", errors.WithStack(err)).Warn("failed to return server to running state")
				}
			} else if r || (!r && s.IsRunning()) {
				// If the server is currently running on Docker, mark the process as being in that state.
				// We never want to stop an instance that is currently running external from Wings since
				// that is a good way of keeping things running even if Wings gets in a very corrupted state.
				//
				// This will also validate that a server process is running if the last tracked state we have
				// is that it was running, but we see that the container process is not currently running.
				s.Log().Info("detected server is running, re-attaching to process...")

				s.SetState(environment.ProcessRunningState)
				if err := s.Environment.Attach(); err != nil {
					s.Log().WithField("error", errors.WithStack(err)).Warn("failed to attach to running server environment")
				}

				return
			}

			// Addresses potentially invalid data in the stored file that can cause Wings to lose
			// track of what the actual server state is.
			_ = s.SetState(environment.ProcessOfflineState)
		})
	}

	// Wait until all of the servers are ready to go before we fire up the SFTP and HTTP servers.
	pool.StopWait()


	// Ensure the archive directory exists.
	if err := os.MkdirAll(c.System.ArchiveDirectory, 0755); err != nil {
		log.WithField("error", err).Error("failed to create archive directory")
	}

	// Ensure the backup directory exists.
	if err := os.MkdirAll(c.System.BackupDirectory, 0755); err != nil {
		log.WithField("error", err).Error("failed to create backup directory")
	}

	log.WithFields(log.Fields{
		"use_ssl":      c.Api.Ssl.Enabled,
		"use_auto_tls": useAutomaticTls && len(tlsHostname) > 0,
		"host_address": c.Api.Host,
		"host_port":    c.Api.Port,
	}).Info("configuring internal webserver")

	// Configure the router.
	r := router.Configure()

	s := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", c.Api.Host, c.Api.Port),
		Handler: r,

		TLSConfig: &tls.Config{
			NextProtos: []string{
				"h2", // enable HTTP/2
				"http/1.1",
			},

			// https://blog.cloudflare.com/exposing-go-on-the-internet
			CipherSuites: []uint16{
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,

				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,

				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,

				tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			},

			PreferServerCipherSuites: true,

			MinVersion: tls.VersionTLS12,
			MaxVersion: tls.VersionTLS13,

			CurvePreferences: []tls.CurveID{
				tls.X25519,
				tls.CurveP256,
			},
			// END https://blog.cloudflare.com/exposing-go-on-the-internet
		},
	}

	// Check if the server should run with TLS but using autocert.
	if useAutomaticTls && len(tlsHostname) > 0 {
		m := autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(path.Join(c.System.RootDirectory, "/.tls-cache")),
			HostPolicy: autocert.HostWhitelist(tlsHostname),
		}

		log.WithField("hostname", tlsHostname).
			Info("webserver is now listening with auto-TLS enabled; certificates will be automatically generated by Let's Encrypt")

		// Hook autocert into the main http server.
		s.TLSConfig.GetCertificate = m.GetCertificate
		s.TLSConfig.NextProtos = append(s.TLSConfig.NextProtos, acme.ALPNProto) // enable tls-alpn ACME challenges

		// Start the autocert server.
		go func() {
			if err := http.ListenAndServe(":http", m.HTTPHandler(nil)); err != nil {
				log.WithError(err).Error("failed to serve autocert http server")
			}
		}()

		// Start the main http server with TLS using autocert.
		if err := s.ListenAndServeTLS("", ""); err != nil {
			log.WithFields(log.Fields{"auto_tls": true, "tls_hostname": tlsHostname, "error": err}).
				Fatal("failed to configure HTTP server using auto-tls")
			os.Exit(1)
		}

		return
	}

	// Check if main http server should run with TLS.
	if c.Api.Ssl.Enabled {
		if err := s.ListenAndServeTLS(c.Api.Ssl.CertificateFile, c.Api.Ssl.KeyFile); err != nil {
			log.WithFields(log.Fields{"auto_tls": false, "error": err}).Fatal("failed to configure HTTPS server")
			os.Exit(1)
		}
		return
	}

	// Run the main http server without TLS.
	s.TLSConfig = nil
	if err := s.ListenAndServe(); err != nil {
		log.WithField("error", err).Fatal("failed to configure HTTP server")
		os.Exit(1)
	}
}

// Execute calls cobra to handle cli commands
func Execute() error {
	return root.Execute()
}

// Configures the global logger for Zap so that we can call it from any location
// in the code without having to pass around a logger instance.
func configureLogging(logDir string, debug bool) error {
	if err := os.MkdirAll(path.Join(logDir, "/install"), 0700); err != nil {
		return errors.WithStack(err)
	}

	p := filepath.Join(logDir, "/wings.log")
	w, err := logrotate.NewFile(p)
	if err != nil {
		panic(errors.Wrap(err, "failed to open process log file"))
	}

	if debug {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}

	log.SetHandler(multi.New(
		cli.Default,
		cli.New(w.File, false),
	))

	log.WithField("path", p).Info("writing log files to disk")

	return nil
}

// Prints the wings logo, nothing special here!
func printLogo() {
	fmt.Printf(colorstring.Color(`
                     ____
__ [blue][bold]Panther[reset] _____/___/_______ _______ ______
\_____\    \/\/    /   /       /  __   /   ___/
   \___\          /   /   /   /  /_/  /___   /
        \___/\___/___/___/___/___    /______/
                            /_______/ [bold]v%s[reset]

Copyright © 2018 - 2020 Dane Everitt & Contributors

 Source:  https://github.com/avatag-host/claws
License:  https://github.com/avatag-host/claws/blob/develop/LICENSE

This software is made available under the terms of the MIT license.
The above copyright notice and this permission notice shall be included
in all copies or substantial portions of the Software.%s`), system.Version, "\n\n")
}

func exitWithConfigurationNotice() {
	var defaultLocation string;
	if runtime.GOOS == "windows" {
		defaultLocation = `C:\Claws\config.yml`;
	} else {
		defaultLocation = `/etc/claws/config.yml`;
	}
	fmt.Print(colorstring.Color(`
[_red_][white][bold]Error: Configuration File Not Found[reset]

CLaws was not able to locate your configuration file, and therefore is not
able to complete its boot process.

Please ensure you have copied your instance configuration file into
the default location, or have provided the --config flag to use a
custom location.

Default Location: `+defaultLocation+`

[yellow]This is not a bug with this software. Please do not make a bug report
for this issue, it will be closed.[reset]

`))
	os.Exit(1)
}
