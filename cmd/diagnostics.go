package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/avatag-host/claws/environment"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os/exec"
	"path"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/AlecAivazis/survey/v2/terminal"
	"github.com/docker/cli/components/engine/pkg/parsers/operatingsystem"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/pkg/parsers/kernel"
	"github.com/avatag-host/claws/config"
	"github.com/avatag-host/claws/system"
	"github.com/spf13/cobra"
)

const DefaultHastebinUrl = "https://hastebin.com/"
const DefaultLogLines = 200

var (
	diagnosticsArgs struct {
		IncludeEndpoints   bool
		IncludeLogs        bool
		ReviewBeforeUpload bool
		HastebinURL        string
		LogLines           int
	}
)

var diagnosticsCmd = &cobra.Command{
	Use:   "diagnostics",
	Short: "Collect diagnostics information.",
	Run:   diagnosticsCmdRun,
}

func init() {
	diagnosticsCmd.PersistentFlags().StringVar(&diagnosticsArgs.HastebinURL, "hastebin-url", DefaultHastebinUrl, "The url of the hastebin instance to use.")
	diagnosticsCmd.PersistentFlags().IntVar(&diagnosticsArgs.LogLines, "log-lines", DefaultLogLines, "The number of log lines to include in the report")
}

// diagnosticsCmdRun collects diagnostics about wings, it's configuration and the node.
// We collect:
// - wings and docker versions
// - relevant parts of daemon configuration
// - the docker debug output
// - running docker containers
// - logs
func diagnosticsCmdRun(cmd *cobra.Command, args []string) {
	questions := []*survey.Question{
		{
			Name:   "IncludeEndpoints",
			Prompt: &survey.Confirm{Message: "Do you want to include endpoints (i.e. the FQDN/IP of your panel)?", Default: false},
		},
		{
			Name:   "IncludeLogs",
			Prompt: &survey.Confirm{Message: "Do you want to include the latest logs?", Default: true},
		},
		{
			Name: "ReviewBeforeUpload",
			Prompt: &survey.Confirm{
				Message: "Do you want to review the collected data before uploading to hastebin.com?",
				Help:    "The data, especially the logs, might contain sensitive information, so you should review it. You will be asked again if you want to upload.",
				Default: true,
			},
		},
	}
	if err := survey.Ask(questions, &diagnosticsArgs); err != nil {
		if err == terminal.InterruptErr {
			return
		}
		panic(err)
	}

	dockerVersion, dockerInfo, dockerErr := getDockerInfo()
	_ = dockerInfo

	output := &strings.Builder{}
	fmt.Fprintln(output, "Panther Claws - Diagnostics Report")
	printHeader(output, "Versions")
	fmt.Fprintln(output, "         claws:", system.Version)
	if dockerErr == nil {
		fmt.Fprintln(output, "Docker:", dockerVersion.Version)
	}
	if v, err := kernel.GetKernelVersion(); err == nil {
		fmt.Fprintln(output, "Kernel:", v)
	}
	if os, err := operatingsystem.GetOperatingSystem(); err == nil {
		fmt.Fprintln(output, "    OS:", os)
	}

	printHeader(output, "Claws Configuration");
	var err error;
	var cfg *config.Configuration;
	if runtime.GOOS == "windows" {
		cfg, err = config.ReadConfiguration(config.DefaultLocationWindows)
	} else {
		cfg, err = config.ReadConfiguration(config.DefaultLocationLinux)
	}
	if cfg != nil {
		fmt.Fprintln(output, "    Panel Location:", redact(cfg.PanelLocation))
		fmt.Fprintln(output, "")
		fmt.Fprintln(output, " Internal Webserver:", redact(cfg.Api.Host), ":", cfg.Api.Port)
		fmt.Fprintln(output, "        SSL Enabled:", cfg.Api.Ssl.Enabled)
		fmt.Fprintln(output, "    SSL Certificate:", redact(cfg.Api.Ssl.CertificateFile))
		fmt.Fprintln(output, "            SSL Key:", redact(cfg.Api.Ssl.KeyFile))
		fmt.Fprintln(output, "")
		fmt.Fprintln(output, "     Root Directory:", cfg.System.RootDirectory)
		fmt.Fprintln(output, "     Logs Directory:", cfg.System.LogDirectory)
		fmt.Fprintln(output, "     Data Directory:", cfg.System.Data)
		fmt.Fprintln(output, "  Archive Directory:", cfg.System.ArchiveDirectory)
		fmt.Fprintln(output, "   Backup Directory:", cfg.System.BackupDirectory)
		fmt.Fprintln(output, "")
		fmt.Fprintln(output, "           Username:", cfg.System.Username)
		fmt.Fprintln(output, "        Server Time:", time.Now().Format(time.RFC1123Z))
		fmt.Fprintln(output, "         Debug Mode:", cfg.Debug)
	} else {
		fmt.Println("Failed to load configuration.", err)
	}

	printHeader(output, "Docker: Info")
	fmt.Fprintln(output, "Server Version:", dockerInfo.ServerVersion)
	fmt.Fprintln(output, "Storage Driver:", dockerInfo.Driver)
	if dockerInfo.DriverStatus != nil {
		for _, pair := range dockerInfo.DriverStatus {
			fmt.Fprintf(output, "  %s: %s\n", pair[0], pair[1])
		}
	}
	if dockerInfo.SystemStatus != nil {
		for _, pair := range dockerInfo.SystemStatus {
			fmt.Fprintf(output, " %s: %s\n", pair[0], pair[1])
		}
	}
	fmt.Fprintln(output, "LoggingDriver:", dockerInfo.LoggingDriver)
	fmt.Fprintln(output, " CgroupDriver:", dockerInfo.CgroupDriver)
	if len(dockerInfo.Warnings) > 0 {
		for _, w := range dockerInfo.Warnings {
			fmt.Fprintln(output, w)
		}
	}

	printHeader(output, "Docker: Running Containers")
	c := exec.Command("docker", "ps")
	if co, err := c.Output(); err == nil {
		output.Write(co)
	} else {
		fmt.Fprint(output, "Couldn't list containers: ", err)
	}

	printHeader(output, "Latest Claws Logs")
	if diagnosticsArgs.IncludeLogs {
		p := "/var/log/claws/claws.log"
		if cfg != nil {
			p = path.Join(cfg.System.LogDirectory, "wings.log")
		}
		if c, err := exec.Command("tail", "-n", strconv.Itoa(diagnosticsArgs.LogLines), p).Output(); err != nil {
			fmt.Fprintln(output, "No logs found or an error occurred.")
		} else {
			fmt.Fprintf(output, "%s\n", string(c))
		}
	} else {
		fmt.Fprintln(output, "Logs redacted.")
	}

	fmt.Println("\n---------------  generated report  ---------------")
	fmt.Println(output.String())
	fmt.Print("---------------   end of report    ---------------\n\n")

	upload := !diagnosticsArgs.ReviewBeforeUpload
	if !upload {
		survey.AskOne(&survey.Confirm{Message: "Upload to " + diagnosticsArgs.HastebinURL + "?", Default: false}, &upload)
	}
	if upload {
		url, err := uploadToHastebin(diagnosticsArgs.HastebinURL, output.String())
		if err == nil {
			fmt.Println("Your report is available here: ", url)
		}
	}
}

func getDockerInfo() (types.Version, types.Info, error) {
	cli, err := environment.DockerClient()
	if err != nil {
		return types.Version{}, types.Info{}, err
	}
	dockerVersion, err := cli.ServerVersion(context.Background())
	if err != nil {
		return types.Version{}, types.Info{}, err
	}
	dockerInfo, err := cli.Info(context.Background())
	if err != nil {
		return types.Version{}, types.Info{}, err
	}
	return dockerVersion, dockerInfo, nil
}

func uploadToHastebin(hbUrl, content string) (string, error) {
	r := strings.NewReader(content)
	u, err := url.Parse(hbUrl)
	if err != nil {
		return "", err
	}
	u.Path = path.Join(u.Path, "documents")
	res, err := http.Post(u.String(), "plain/text", r)
	if err != nil || res.StatusCode != 200 {
		fmt.Println("Failed to upload report to ", u.String(), err)
		return "", err
	}
	pres := make(map[string]interface{})
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		fmt.Println("Failed to parse response.", err)
		return "", err
	}
	json.Unmarshal(body, &pres)
	if key, ok := pres["key"].(string); ok {
		u, _ := url.Parse(hbUrl)
		u.Path = path.Join(u.Path, key)
		return u.String(), nil
	}
	return "", errors.New("failed to find key in response")
}

func redact(s string) string {
	if !diagnosticsArgs.IncludeEndpoints {
		return "{redacted}"
	}
	return s
}

func printHeader(w io.Writer, title string) {
	fmt.Fprintln(w, "\n|\n|", title)
	fmt.Fprintln(w, "| ------------------------------")
}
