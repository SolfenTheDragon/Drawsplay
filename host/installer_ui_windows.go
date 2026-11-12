//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

type installerChoices struct {
	Desktop   bool `json:"desktop"`
	Autostart bool `json:"autostart"`
	Driver    bool `json:"driver"`
}

func chooseInstallerOptions(o installerOptions, desktop, autostart, driverPresent bool) (installerChoices, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := powerShellCommand(ctx, installerChoicesScript,
		"DS_INSTALL_DIR="+defaultInstallDir(),
		"DS_DESKTOP="+strconv.FormatBool(desktop),
		"DS_AUTOSTART="+strconv.FormatBool(autostart),
		"DS_DRIVER="+strconv.FormatBool(driverPresent && !o.SkipVDD),
		"DS_DESKTOP_LOCKED="+strconv.FormatBool(o.DesktopShortcut),
		"DS_AUTOSTART_LOCKED="+strconv.FormatBool(o.Autostart || o.NoAutostart),
		"DS_DRIVER_LOCKED="+strconv.FormatBool(o.SkipVDD || driverPresent),
	)
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 2 {
			return installerChoices{}, false, nil
		}
		return installerChoices{}, false, fmt.Errorf("open setup choices: %w", err)
	}
	var choices installerChoices
	if err := json.Unmarshal(out, &choices); err != nil {
		return choices, false, fmt.Errorf("read setup choices: %w", err)
	}
	return choices, true, nil
}

type installerProgress struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	cancel context.CancelFunc
	once   sync.Once
}

func startInstallerProgress(quiet bool) *installerProgress {
	if quiet {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), installElevateTimeout+2*time.Minute)
	cmd := powerShellCommand(ctx, installerProgressScript)
	input, err := cmd.StdinPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		if input != nil {
			_ = input.Close()
		}
		cancel()
		log.Printf("Setup progress window unavailable: %v", err)
		return nil
	}
	return &installerProgress{cmd: cmd, input: input, cancel: cancel}
}

func (p *installerProgress) update(text string) {
	fmt.Println(text)
	if p != nil {
		_, _ = fmt.Fprintln(p.input, text)
	}
}

func (p *installerProgress) close() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		_ = p.input.Close()
		done := make(chan struct{})
		go func() { _ = p.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			p.cancel()
			<-done
		}
		p.cancel()
	})
}

// Paths and choices are data passed through the environment, never inserted
// into PowerShell source. These windows run as the installing user, not admin.
const installerChoicesScript = `
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
[System.Windows.Forms.Application]::EnableVisualStyles()
$form = New-Object System.Windows.Forms.Form
$form.Text = 'Install DrawSplay'
$form.Font = New-Object System.Drawing.Font('Segoe UI', 10)
$form.AutoScaleMode = 'Dpi'
$form.ClientSize = New-Object System.Drawing.Size(660, 570)
$form.MinimumSize = $form.Size
$form.StartPosition = 'CenterScreen'
$layout = New-Object System.Windows.Forms.TableLayoutPanel
$layout.Dock = 'Fill'
$layout.AutoScroll = $true
$layout.Padding = New-Object System.Windows.Forms.Padding(24)
$layout.ColumnCount = 1
$layout.RowCount = 7
$form.Controls.Add($layout)
function Add-Text($text) {
  $label = New-Object System.Windows.Forms.Label
  $label.Text = $text
  $label.AutoSize = $true
  $label.MaximumSize = New-Object System.Drawing.Size(600, 0)
  $label.Margin = New-Object System.Windows.Forms.Padding(0, 0, 0, 16)
  $layout.Controls.Add($label)
}
function Add-Choice($text, $checked, $locked) {
  $box = New-Object System.Windows.Forms.CheckBox
  $box.Text = $text
  $box.AutoSize = $true
  $box.MinimumSize = New-Object System.Drawing.Size(0, 40)
  $box.Checked = ($checked -eq 'true')
  $box.Enabled = ($locked -ne 'true')
  $layout.Controls.Add($box)
  return $box
}
Add-Text ("Use your tablet as a display and drawing surface.` + "`n`n" + `Install location: " + $env:DS_INSTALL_DIR)
$desktop = Add-Choice '&Add a desktop shortcut' $env:DS_DESKTOP $env:DS_DESKTOP_LOCKED
$autostart = Add-Choice '&Start in the system tray when I sign in' $env:DS_AUTOSTART $env:DS_AUTOSTART_LOCKED
$driver = Add-Choice 'Enable an &extra screen (virtual display driver)' $env:DS_DRIVER $env:DS_DRIVER_LOCKED
Add-Text 'Leave extra screen off to mirror your screen without installing a display driver. If already installed, your driver is kept.'
Add-Text "Extra screen may add a local signing certificate to this PC's Trusted Root and Trusted Publishers stores. The private key is deleted immediately; uninstall removes the driver and certificate. Secure Boot and signature enforcement are not changed. A verified Parsec fallback is installed only if needed."
Add-Text 'Windows will ask for administrator approval. Installing drivers may take several minutes. You can change startup and display preferences later.'
$buttons = New-Object System.Windows.Forms.FlowLayoutPanel
$buttons.FlowDirection = 'RightToLeft'
$buttons.WrapContents = $false
$buttons.Dock = 'Bottom'
$buttons.Height = 64
$buttons.Padding = New-Object System.Windows.Forms.Padding(12)
$install = New-Object System.Windows.Forms.Button
$install.Text = '&Install'
$install.AutoSize = $true
$install.MinimumSize = New-Object System.Drawing.Size(100, 40)
$install.DialogResult = 'OK'
$cancel = New-Object System.Windows.Forms.Button
$cancel.Text = 'Cancel'
$cancel.AutoSize = $true
$cancel.MinimumSize = New-Object System.Drawing.Size(100, 40)
$cancel.DialogResult = 'Cancel'
$buttons.Controls.Add($install)
$buttons.Controls.Add($cancel)
$form.Controls.Add($buttons)
$buttons.BringToFront()
$form.AcceptButton = $install
$form.CancelButton = $cancel
if ($form.ShowDialog() -ne 'OK') { $form.Dispose(); exit 2 }
@{desktop=$desktop.Checked; autostart=$autostart.Checked; driver=$driver.Checked} | ConvertTo-Json -Compress
$form.Dispose()
`

const installerProgressScript = `
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
Add-Type -TypeDefinition @'
using System;
using System.Collections.Concurrent;
using System.Threading;
public static class DrawSplaySetupLines {
  public static readonly ConcurrentQueue<string> Lines = new ConcurrentQueue<string>();
  public static volatile bool Closed;
  public static void Start() {
    var thread = new Thread(() => {
      try {
        string line;
        while ((line = Console.ReadLine()) != null) Lines.Enqueue(line);
      } finally { Closed = true; }
    });
    thread.IsBackground = true;
    thread.Start();
  }
}
'@
[System.Windows.Forms.Application]::EnableVisualStyles()
$form = New-Object System.Windows.Forms.Form
$form.Text = 'Installing DrawSplay'
$form.Font = New-Object System.Drawing.Font('Segoe UI', 10)
$form.AutoScaleMode = 'Dpi'
$form.ClientSize = New-Object System.Drawing.Size(560, 210)
$form.FormBorderStyle = 'FixedDialog'
$form.ControlBox = $false
$form.StartPosition = 'CenterScreen'
$label = New-Object System.Windows.Forms.Label
$label.Text = 'Preparing setup...'
$label.Location = New-Object System.Drawing.Point(24, 24)
$label.Size = New-Object System.Drawing.Size(512, 80)
$label.AccessibleName = 'Installation status'
$bar = New-Object System.Windows.Forms.ProgressBar
$bar.Location = New-Object System.Drawing.Point(24, 118)
$bar.Size = New-Object System.Drawing.Size(512, 18)
$bar.Style = 'Marquee'
$bar.AccessibleName = 'Installation in progress'
$note = New-Object System.Windows.Forms.Label
$note.Text = 'Please keep this window open. Setup will show the result when finished.'
$note.Location = New-Object System.Drawing.Point(24, 150)
$note.Size = New-Object System.Drawing.Size(512, 45)
$form.Controls.AddRange(@($label, $bar, $note))
$script:closing = $false
$form.Add_FormClosing({ param($sender, $eventArgs) if (-not $script:closing) { $eventArgs.Cancel = $true } })
[DrawSplaySetupLines]::Start()
$timer = New-Object System.Windows.Forms.Timer
$timer.Interval = 200
$timer.Add_Tick({
  $line = ''
  while ([DrawSplaySetupLines]::Lines.TryDequeue([ref]$line)) { $label.Text = $line }
  if ([DrawSplaySetupLines]::Closed) {
    $script:closing = $true
    $form.Close()
  }
})
$timer.Start()
[void]$form.ShowDialog()
$timer.Dispose()
$form.Dispose()
`
