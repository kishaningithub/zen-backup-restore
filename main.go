// Command zen-backup-restore backs up and restores Zen Browser profiles to
// local .tar.gz archives, no account or cloud sync required.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/kishaningithub/zen-backup-restore/internal/backup"
	"github.com/kishaningithub/zen-backup-restore/internal/zen"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Version is set at build time by goreleaser.
var Version = "dev"

func defaultBackupDir() string {
	if d := os.Getenv("ZEN_BACKUP_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Backups", "ZenBrowser")
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type config struct {
	zenDir    string
	backupDir string
	profile   string
	output    string
	newName   string
	slim      bool
	force     bool
	noKeepOld bool
	yes       bool
}

func newRootCmd() *cobra.Command {
	var c config
	root := &cobra.Command{
		Use:   "zen-backup-restore",
		Short: "Back up and restore Zen Browser profiles locally",
		Long: `Back up and restore Zen Browser profiles to local .tar.gz archives,
no account or online sync required.

Run without a command for an interactive menu.`,
		Version:       Version,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ui := newUI()
			if !ui.tty {
				return cmd.Help()
			}
			action, err := choose("Zen Backup & Restore — what do you want to do?",
				huh.NewOption("Back up a profile", "backup"),
				huh.NewOption("Restore a backup", "restore"),
				huh.NewOption("List profiles and backups", "list"),
			)
			if err != nil {
				return err
			}
			switch action {
			case "backup":
				return cmdBackup(c, ui)
			case "restore":
				return cmdRestore(c, ui, "")
			default:
				return cmdList(c)
			}
		},
	}
	root.SetVersionTemplate("{{.Version}}\n")
	pf := root.PersistentFlags()
	pf.StringVar(&c.zenDir, "zen-dir", "", "Zen data dir containing profiles.ini (auto-detected)")
	pf.StringVar(&c.backupDir, "backup-dir", defaultBackupDir(), "where backups are stored (env ZEN_BACKUP_DIR)")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "Show profiles and existing backups",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return cmdList(c) },
	}

	backupCmd := &cobra.Command{
		Use:   "backup",
		Short: "Back up a profile",
		Example: `  zen-backup-restore backup
  zen-backup-restore backup -p "Default (release)" --slim -o ~/Dropbox/zen`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return cmdBackup(c, newUI()) },
	}
	bf := backupCmd.Flags()
	bf.StringVarP(&c.profile, "profile", "p", "", "profile name or path to back up (default: Zen's default profile)")
	bf.StringVarP(&c.output, "output", "o", "", "backup directory or .tar.gz path (default: --backup-dir)")
	bf.BoolVar(&c.slim, "slim", false, "skip website storage and old session snapshots")
	bf.BoolVar(&c.force, "force", false, "back up even if Zen is running (may be inconsistent)")

	restoreCmd := &cobra.Command{
		Use:   "restore [archive]",
		Short: "Restore a backup over a profile or into a new one",
		Long: `Restore a backup over an existing profile (-p) or into a new profile (-n).
Without an archive, pick one interactively from --backup-dir.`,
		Example: `  zen-backup-restore restore
  zen-backup-restore restore backup.tar.gz --new "Restored"
  zen-backup-restore restore backup.tar.gz -p "Default (release)" -y`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return cmdRestore(c, newUI(), lo.FirstOr(args, ""))
		},
	}
	rf := restoreCmd.Flags()
	rf.StringVarP(&c.profile, "profile", "p", "", "existing profile name or path to overwrite")
	rf.StringVarP(&c.newName, "new", "n", "", "create and register a new profile with this name")
	rf.BoolVar(&c.noKeepOld, "no-keep-old", false, "delete the overwritten profile instead of keeping a copy")
	rf.BoolVarP(&c.yes, "yes", "y", false, "don't ask for confirmation")
	restoreCmd.MarkFlagsMutuallyExclusive("profile", "new")

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run:   func(*cobra.Command, []string) { fmt.Println(Version) },
	}

	root.AddCommand(listCmd, backupCmd, restoreCmd, versionCmd)
	return root
}

func zenRoot(c config) (string, error) {
	if c.zenDir != "" {
		return c.zenDir, nil
	}
	return zen.FindRoot()
}

func loadProfiles(c config) (string, []zen.Profile, error) {
	root, err := zenRoot(c)
	if err != nil {
		return "", nil, err
	}
	profiles, err := zen.ListProfiles(root)
	if err != nil {
		return "", nil, fmt.Errorf("reading profiles.ini: %w", err)
	}
	return root, profiles, nil
}

func isUsedProfile(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "places.sqlite"))
	return err == nil
}

func profileLabel(p zen.Profile) string {
	tags := lo.Compact([]string{
		lo.Ternary(p.Default, "default", ""),
		lo.Ternary(zen.InUse(p.Path), "in use", ""),
		lo.Ternary(isUsedProfile(p.Path), "", "empty"),
	})
	label := p.Name
	if len(tags) > 0 {
		label += " [" + strings.Join(tags, ", ") + "]"
	}
	return label + "\n      " + p.Path
}

func pickProfile(ui *ui, profiles []zen.Profile, query, prompt string) (zen.Profile, error) {
	if query != "" {
		return zen.FindProfile(profiles, query)
	}
	if len(profiles) == 0 {
		return zen.Profile{}, errors.New("no profiles found in profiles.ini")
	}
	if len(profiles) == 1 || !ui.tty {
		return profiles[0], nil // sorted with the default first
	}
	return choose(prompt, lo.Map(profiles, func(p zen.Profile, _ int) huh.Option[zen.Profile] {
		return huh.NewOption(profileLabel(p), p)
	})...)
}

func cmdList(c config) error {
	root, profiles, err := loadProfiles(c)
	if err != nil {
		return err
	}
	fmt.Printf("Zen data dir: %s\n\nProfiles:\n", root)
	for _, p := range profiles {
		fmt.Printf("  - %s\n", profileLabel(p))
	}

	backups := findBackups(c.backupDir)
	fmt.Printf("\nBackups in %s:\n", c.backupDir)
	if len(backups) == 0 {
		fmt.Println("  (none)")
	}
	for _, b := range backups {
		fmt.Printf("  - %s\n", backupLabel(b))
	}
	return nil
}

func cmdBackup(c config, ui *ui) error {
	_, profiles, err := loadProfiles(c)
	if err != nil && c.profile == "" {
		return err
	}
	p, err := pickProfile(ui, profiles, c.profile, "Which profile do you want to back up?")
	if err != nil {
		return err
	}
	if !isUsedProfile(p.Path) {
		return fmt.Errorf("%s doesn't look like a used Zen profile (no places.sqlite)", p.Path)
	}
	if zen.InUse(p.Path) {
		if !c.force {
			return errors.New("zen is running with this profile; quit Zen first so databases are consistent (or pass --force)")
		}
		fmt.Fprintln(os.Stderr, "warning: Zen is running; the backup may be inconsistent")
	}

	out := archivePath(c, p.Name)
	fmt.Printf("Backing up %q\n  from %s\n  to   %s\n", p.Name, p.Path, out)
	m, err := backup.Create(p.Path, p.Name, zen.Version(p.Path), out, backup.Options{Slim: c.slim}, progressPrinter())
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	fmt.Printf("Done: %d files, %s of profile data -> %s archive\n%s\n",
		m.Files, humanBytes(m.Bytes), humanBytes(fileSize(out)), out)
	return nil
}

// archivePath returns where to write a new backup: --output if it names an
// archive, otherwise a timestamped file in --output or --backup-dir.
func archivePath(c config, profileName string) string {
	if strings.HasSuffix(c.output, ".tar.gz") || strings.HasSuffix(c.output, ".tgz") {
		return c.output
	}
	dir := lo.Ternary(c.output != "", c.output, c.backupDir)
	return filepath.Join(dir, backup.ArchiveName(profileName, time.Now()))
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

type backupInfo struct {
	path     string
	manifest backup.Manifest
	err      error
}

// findBackups lists the archives in dir, newest first.
func findBackups(dir string) []backupInfo {
	paths := lo.FlatMap([]string{"*.tar.gz", "*.tgz"}, func(pattern string, _ int) []string {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		return matches
	})
	backups := lo.Map(paths, func(p string, _ int) backupInfo {
		m, err := backup.ReadManifest(p)
		return backupInfo{p, m, err}
	})
	slices.SortFunc(backups, func(a, b backupInfo) int { return b.manifest.CreatedAt.Compare(a.manifest.CreatedAt) })
	return backups
}

func backupLabel(b backupInfo) string {
	name := filepath.Base(b.path)
	if b.err != nil {
		return name + " (unreadable: " + b.err.Error() + ")"
	}
	m := b.manifest
	details := lo.Compact([]string{
		fmt.Sprintf("profile %q from %s", m.ProfileName, m.OS),
		m.CreatedAt.Local().Format("2006-01-02 15:04"),
		lo.Ternary(m.ZenVersion != "", "Zen "+m.ZenVersion, ""),
		lo.Ternary(m.Slim, "slim", ""),
		humanBytes(m.Bytes),
	})
	return name + "\n      " + strings.Join(details, ", ")
}

func cmdRestore(c config, ui *ui, archive string) error {
	if archive == "" {
		var err error
		if archive, err = chooseArchive(c, ui); err != nil {
			return err
		}
	}
	m, err := backup.ReadManifest(archive)
	if err != nil {
		return err
	}
	root, profiles, err := loadProfiles(c)
	if err != nil {
		return err
	}

	target, newName, err := chooseRestoreTarget(c, ui, profiles, m.ProfileName+" restored")
	if err != nil {
		return err
	}
	if newName != "" {
		return restoreIntoNewProfile(root, profiles, archive, newName)
	}
	return restoreOverProfile(c, ui, archive, target)
}

func chooseArchive(c config, ui *ui) (string, error) {
	backups := lo.Filter(findBackups(c.backupDir), func(b backupInfo, _ int) bool { return b.err == nil })
	if len(backups) == 0 {
		return "", fmt.Errorf("no backups found in %s; pass the archive path explicitly", c.backupDir)
	}
	if !ui.tty {
		return "", errors.New("specify the archive to restore")
	}
	return choose("Which backup do you want to restore?", lo.Map(backups, func(b backupInfo, _ int) huh.Option[string] {
		return huh.NewOption(backupLabel(b), b.path)
	})...)
}

// chooseRestoreTarget returns either an existing profile to overwrite or the
// name of a new profile to create, from flags or by asking.
func chooseRestoreTarget(c config, ui *ui, profiles []zen.Profile, defaultNewName string) (zen.Profile, string, error) {
	switch {
	case c.newName != "":
		return zen.Profile{}, c.newName, nil
	case c.profile != "":
		target, err := zen.FindProfile(profiles, c.profile)
		return target, "", err
	case !ui.tty:
		return zen.Profile{}, "", errors.New("specify --profile to overwrite or --new to create a profile")
	}

	// A nil choice means "create a new profile".
	overwriteOptions := lo.Map(profiles, func(p zen.Profile, i int) huh.Option[*zen.Profile] {
		return huh.NewOption("Overwrite "+profileLabel(p), &profiles[i])
	})
	target, err := choose("Restore into:",
		append([]huh.Option[*zen.Profile]{huh.NewOption[*zen.Profile]("Create a new profile (safest)", nil)}, overwriteOptions...)...)
	if err != nil {
		return zen.Profile{}, "", err
	}
	if target != nil {
		return *target, "", nil
	}
	name, err := ask("Name for the new profile", defaultNewName)
	return zen.Profile{}, name, err
}

func restoreIntoNewProfile(root string, profiles []zen.Profile, archive, name string) error {
	if lo.SomeBy(profiles, func(p zen.Profile) bool { return zen.InUse(p.Path) }) {
		return errors.New("quit Zen before creating a new profile (it rewrites profiles.ini on exit)")
	}
	if lo.SomeBy(profiles, func(p zen.Profile) bool { return strings.EqualFold(p.Name, name) }) {
		return fmt.Errorf("a profile named %q already exists", name)
	}
	dir, err := zen.NewProfileDir(root, sanitizeDirName(name))
	if err != nil {
		return err
	}

	fmt.Printf("Restoring %s\n  into new profile %q at %s\n", filepath.Base(archive), name, dir)
	_, err = backup.Restore(archive, dir, false, progressPrinter())
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	if err := zen.RegisterProfile(root, name, dir); err != nil {
		return fmt.Errorf("profile restored to %s but registering it in profiles.ini failed: %w", dir, err)
	}
	fmt.Printf("Done. Pick %q in about:profiles, or launch with:\n  zen --profile %q\n", name, dir)
	return nil
}

func restoreOverProfile(c config, ui *ui, archive string, target zen.Profile) error {
	if zen.InUse(target.Path) {
		return fmt.Errorf("zen is running with profile %q; quit it first", target.Name)
	}
	fmt.Printf("Restoring %s\n  over profile %q at %s\n", filepath.Base(archive), target.Name, target.Path)
	if c.noKeepOld {
		fmt.Println("  WARNING: the current profile will be deleted")
	} else {
		fmt.Println("  (the current profile will be kept as a .pre-restore copy)")
	}
	if err := confirmOverwrite(c, ui); err != nil {
		return err
	}

	result, err := backup.Restore(archive, target.Path, !c.noKeepOld, progressPrinter())
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	fmt.Println("Done. Start Zen to use the restored profile.")
	if result.PreviousDir != "" {
		fmt.Printf("Previous profile kept at:\n  %s\nDelete it once you're happy with the restore.\n", result.PreviousDir)
	}
	return nil
}

func confirmOverwrite(c config, ui *ui) error {
	if c.yes {
		return nil
	}
	if !ui.tty {
		return errors.New("refusing to overwrite without confirmation; pass --yes")
	}
	ok, err := confirm("Continue?")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("aborted")
	}
	return nil
}

var badDirChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]+`)

func sanitizeDirName(s string) string {
	s = strings.TrimSpace(badDirChars.ReplaceAllString(s, "-"))
	return lo.Ternary(s != "", s, "restored")
}

func progressPrinter() backup.Progress {
	if !term.IsTerminal(int(os.Stderr.Fd())) {
		return nil
	}
	return func(files int, bytes int64) {
		fmt.Fprintf(os.Stderr, "\r  %d files, %s ", files, humanBytes(bytes))
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
