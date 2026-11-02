package ctl

import (
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// commands are every command with what it does, in the order help lists
// them; a blank name starts a new group.
var commands = []struct{ name, summary string }{
	{"status", "show this device: Device ID, networks, sessions, requests"},
	{"password", "show the temporary password"},
	{"regenerate", "make a new temporary password"},
	{"permanent", "set the permanent password (KYOKAI_PERMANENT_PASSWORD), or --remove it"},
	{"name-set", "set this device's friendly name, and whether the networks see it"},
	{"access", "choose which passwords open this host and who may ask"},
	{"password-grants", "choose what a correct password grants without asking"},
	{"trusted", "list the devices trusted with this host"},
	{"untrust", "stop trusting a device's key"},
	{"backup", "write this device's identity to a file (KYOKAI_BACKUP_PASSPHRASE)"},
	{"restore", "bring an identity back from a backup file (KYOKAI_BACKUP_PASSPHRASE)"},
	{"reset-identity", "give this device a new Device ID (--yes)"},
	{"", ""},
	{"network", "show the first network's settings"},
	{"network-set", "set the first network (secret from KYOKAI_NETWORK_SECRET)"},
	{"networks", "list every network kept"},
	{"network-profile", "add or change a network (secret from KYOKAI_NETWORK_SECRET)"},
	{"network-remove", "forget a network"},
	{"network-direct", "try direct paths in every network (--on), or not"},
	{"network-backend", "run the networks on an official EasyTier core, or on Kyokai Network"},
	{"cores", "list the official EasyTier cores, downloaded and available"},
	{"core-install", "download an official EasyTier core"},
	{"", ""},
	{"connect", "ask a host for a session (password from KYOKAI_PASSWORD)"},
	{"identify", "find which device answers at an address"},
	{"close", "end a session"},
	{"allow", "let a connection request in"},
	{"deny", "turn a connection request away"},
	{"forget", "forget a host"},
	{"remove", "remove a device from the list"},
	{"screenshot", "save a controlled host's screen as a PNG file"},
	{"watch", "watch a controlled host's stream and report on it (for testing)"},
	{"watch-status", "watch hosts' status, printing each sample"},
	{"", ""},
	{"ai", "show the AI settings"},
	{"ai-set", "choose the model, the approval mode or the AI sources"},
	{"ai-key", "save an AI source's key (KYOKAI_AI_KEY), or --remove it"},
	{"ai-models", "list an AI source's models"},
	{"ai-start", "start an AI task on this device"},
	{"ai-reply", "answer an AI task's question"},
	{"ai-tasks", "list the AI tasks"},
	{"ai-rename", "name an AI task"},
	{"ai-export", "write an AI task as Markdown, without screenshots"},
	{"ai-pause", "pause the AI task"},
	{"ai-resume", "resume the AI task"},
	{"ai-cancel", "cancel the AI task"},
	{"ai-approve", "approve the action the AI waits on"},
	{"ai-decline", "decline the action the AI waits on"},
	{"ai-apps", "list the programs with windows on the screen"},
	{"ai-scope", "let the AI work only in these programs (--apps), or anywhere"},
	{"ai-clear", "clear the AI history"},
	{"ai-stop", "stop all AI work at once"},
	{"", ""},
	{"keys", "list this device's sign-in keys"},
	{"keys-generate", "make a sign-in key"},
	{"keys-remove", "remove a sign-in key"},
	{"authorized", "list the keys that may sign in to this device"},
	{"authorized-add", "let a key sign in to this device"},
	{"authorized-remove", "stop a key signing in to this device"},
	{"tunnel", "pipe a host's SSH, Remote Desktop or VNC to standard input and output"},
	{"credentials-set", "save a host service's sign-in (password from KYOKAI_SERVICE_PASSWORD)"},
	{"credentials-get", "show a saved sign-in, never its password"},
	{"credentials-forget", "forget saved sign-ins"},
	{"", ""},
	{"background", "keep the privileged helper serving from boot (--on), or only with the app"},
	{"helper-update", "have the helper install the app's signed bundle"},
	{"move", "move this device's store to another engine, which stops this one"},
	{"capture-show", "show Kyokai's own windows in captures (--on), or hide them"},
	{"no-logs", "keep no logs or connection history (--on), or keep them"},
	{"protect", "turn protected mode on (--on), or off (KYOKAI_PERMANENT_PASSWORD)"},
	{"clipboard-mine", "tell whether the clipboard holds what this device put there"},
	{"logs", "print the engine's logs"},
	{"shutdown", "stop the engine"},
	{"", ""},
	{"help", "list the commands, or explain one: help COMMAND"},
	{"version", "print the version"},
}

// streams are the commands that print as things arrive, which --json does
// not cover.
var streams = map[string]bool{"watch": true, "watch-status": true, "tunnel": true, "logs": true}

// summary is what cmd does; empty for an unknown command.
func summary(cmd string) string {
	for _, c := range commands {
		if c.name == cmd && cmd != "" {
			return c.summary
		}
	}
	return ""
}

// overview writes every command with what it does.
func overview(w io.Writer) {
	fmt.Fprintf(w, "usage: %s COMMAND [flags]\n", prog)
	for _, c := range commands {
		if c.name == "" {
			fmt.Fprintln(w)
			continue
		}
		fmt.Fprintf(w, "  %-20s %s\n", c.name, c.summary)
	}
	fmt.Fprintf(w, "\n%s help COMMAND explains a command and its flags. Exit codes: 0 done, 1 failed,\n2 wrong usage, 3 the engine is not running, 4 the engine refused.\n", prog)
}

// help answers `help` and `help COMMAND`.
func help(args []string) error {
	switch {
	case len(args) == 0:
		overview(os.Stdout)
		return nil
	case len(args) > 1 || summary(args[0]) == "":
		overview(os.Stderr)
		return errUsage
	}
	cmd := args[0]
	if f, ok := ctlServices[cmd]; ok {
		return f([]string{cmd, "-help"})
	}
	if cmd == "help" || cmd == "version" {
		fmt.Printf("%s %s: %s\n", prog, cmd, summary(cmd))
		return nil
	}
	return run([]string{cmd, "-help"})
}

// commandHelp writes what cmd does and the flags it takes: those whose
// description starts by naming it ("ai-set, ai-key: …") and those every
// command takes.
func commandHelp(fs *flag.FlagSet, cmd string) {
	w := fs.Output()
	fmt.Fprintf(w, "usage: %s %s [flags]\n  %s\n", prog, cmd, summary(cmd))
	fs.VisitAll(func(f *flag.Flag) {
		text, ok := flagFor(f.Usage, cmd)
		if !ok {
			return
		}
		arg := ""
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !b.IsBoolFlag() {
			arg = " VALUE"
		}
		fmt.Fprintf(w, "  --%s%s\n      %s", f.Name, arg, text)
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" && f.Name != "socket" {
			fmt.Fprintf(w, " (default %s)", f.DefValue)
		}
		fmt.Fprintln(w)
	})
}

// flagFor reads a flag's description for cmd. A description made of
// clauses that each start by naming commands ("ai-key, ai-models: the
// source; ai-set: …") applies to those commands, each seeing its own
// clauses; one that names none applies to every command.
// Returns: the text cmd shows, and whether the flag applies to cmd.
func flagFor(usage, cmd string) (string, bool) {
	var mine []string
	named, match := false, false
	for clause := range strings.SplitSeq(usage, "; ") {
		head, text, ok := strings.Cut(clause, ": ")
		names := strings.Split(head, ", ")
		if ok && !slices.ContainsFunc(names, func(n string) bool { return summary(n) == "" }) {
			named, match = true, slices.Contains(names, cmd)
			clause = text
		}
		if match {
			mine = append(mine, clause)
		}
	}
	if !named {
		return usage, true
	}
	return strings.Join(mine, "; "), len(mine) > 0
}
