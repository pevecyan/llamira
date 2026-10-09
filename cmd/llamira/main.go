// Command llamira manages a llama.cpp router: `serve` runs on the llama host,
// `connect` and the other commands run anywhere and reach hosts over SSH.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"llamira/internal/config"
	"llamira/internal/install"
)

var version = "dev"

const usage = `Llamira — your local AI model control center (llama.cpp router management)

On the llama host:
  serve      [--config FILE]                 run the API + UI (normally via systemd)
  install    [--config FILE] [--adopt] [--yes] [--listen ADDR] [--auth token|none]
             [--tls self-signed|off] [--read-token] [--llama-cpp prebuilt|source] [--llama-ref TAG]
                                             install service user, sudoers rule and systemd unit;
                                             without llama-server: installs llama.cpp + the router;
                                             --adopt moves per-model flags into the preset file
  uninstall  [--purge-models] [--remove-packages] [--yes]
                                             remove llamira (models are kept by default)
  detect                                     show what install would detect (OS, GPUs, CUDA)
  password   [--config FILE] [--off]         set (or remove) a password for the UI on the LAN,
                                             next to the token login

From anywhere (HOST is a name from hosts.yaml or user@host[:port]):
  connect    [HOST...] [--listen ADDR] [--no-browser]
                                             open the UI for one or more hosts over SSH
  deploy     HOST [--adopt] [--yes] [--listen ADDR] [--auth token|none] [--tls self-signed|off]
             [--read-token] [--binary FILE]
                                             upload this binary to HOST and run install there;
                                             --listen 0.0.0.0:7070 serves the UI on the LAN (token login,
                                             or --auth none for no authentication); --tls self-signed
                                             serves it over HTTPS; --read-token adds a GET-only token
  hosts      [add NAME user@host[:port] | rm NAME | default NAME]
                                             list or edit ~/.config/llamira/hosts.yaml

  status     [--host HOST] [--json]          router, service and GPU status
  models     [--host HOST] [--json]          list models
  load       ID [--host HOST]                load a model
  unload     ID [--host HOST]                unload a model
  download   ORG/REPO:QUANT [--preset [--ctx N] [--device CUDA0] [--embeddings]] [--wait]
                                             download a model through the router (optionally + preset)
  delete     ID [--yes]                      delete a downloaded model from disk
  bench      ID [--prompt N] [--gen N] [--no-restore] [--json]
                                             speed test (stored like the Benchmarks page)
  usage      [--days N] [--json]             requests and tokens per model
  power      [GPU WATTS|GPU default] [--json]
                                             list or set (and keep) GPU power limits
  aliases    [--json]                        alias → model
  export     [--secrets] [-o FILE]           presets, schedules, alerts, power limits, config (JSON)
  import     FILE [--parts presets,schedules,alerts,power] [--yes]
                                             restore an export (presets go through review & apply)
  presets    [--host HOST]                   print the preset file
  apply      FILE [--host HOST] [--yes] [--check]
                                             validate, preview and apply a preset file
  logs       [--host HOST] [-n N] [-f]       llama-server journal
  restart    [--host HOST]                   restart the llama-server service
  doctor     [--host HOST] [--json]          check the setup and say how to fix problems
                                             (exit code 1 when a check fails)
  inspect    FILE.gguf                       print GGUF metadata and per-layer tensor sizes
  version

Without --host, commands use the local socket (on the llama host) or the default host.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "serve":
		err = cmdServe(args)
	case "install":
		err = cmdInstall(args)
	case "password":
		err = cmdPassword(args)
	case "connect":
		err = cmdConnect(args)
	case "deploy":
		err = cmdDeploy(args)
	case "hosts":
		err = cmdHosts(args)
	case "status":
		err = cmdStatus(args)
	case "models", "ls":
		err = cmdModels(args)
	case "load", "unload":
		err = cmdLoadUnload(cmd, args)
	case "presets":
		err = cmdPresets(args)
	case "apply":
		err = cmdApply(args)
	case "logs":
		err = cmdLogs(args)
	case "restart":
		err = cmdRestart(args)
	case "doctor":
		err = cmdDoctor(args)
	case "download":
		err = cmdDownload(args)
	case "delete", "rm":
		err = cmdDelete(args)
	case "bench":
		err = cmdBench(args)
	case "usage":
		err = cmdUsage(args)
	case "power":
		err = cmdPower(args)
	case "aliases":
		err = cmdAliases(args)
	case "export":
		err = cmdExport(args)
	case "import":
		err = cmdImport(args)
	case "privileged":
		// Always the root-owned default config: the sudo rule allows any arguments here.
		err = install.Privileged(context.Background(), config.DefaultServerConfig, args, os.Stdout)
	case "llamacpp-build":
		err = cmdLlamacppBuild(args)
	case "llamacpp-fetch":
		err = cmdLlamacppFetch(args)
	case "uninstall":
		err = cmdUninstall(args)
	case "detect":
		err = cmdDetect(args)
	case "inspect":
		err = cmdInspect(args)
	case "version", "--version", "-v":
		fmt.Println("Llamira", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// parseArgs parses flags that may appear before or after positional arguments.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func confirm(q string) bool {
	fmt.Fprintf(os.Stderr, "%s [y/N] ", q)
	var ans string
	fmt.Scanln(&ans)
	return strings.EqualFold(strings.TrimSpace(ans), "y")
}
