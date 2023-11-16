package psscanner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"regexp"
	"golang.org/x/exp/slices"
	"strconv"
	"strings"
	"syscall"
)

type PSScanner struct {
	enablePpid   bool
	eventCh      chan<- PSEvent
	maxCmdLength int
	cgroupFilter string
	userFilter []string
	cmdFilter []string
}

type PSEvent struct {
	UID  int
	PID  int
	PPID int
	CMD  string
}

func (evt PSEvent) String() string {
	uid := strconv.Itoa(evt.UID)
	if evt.UID == -1 {
		uid = "???"
	}
	// strip whitespace from CMD
	evt.CMD = strings.TrimSpace(evt.CMD)

	if evt.PPID == -1 {
		return fmt.Sprintf("UID=%-5s PID=%-6d CMD=%s", uid, evt.PID, evt.CMD)
	}

	return fmt.Sprintf(
		"UID=%-5s PID=%-6d PPID=%-6d CMD=%s", uid, evt.PID, evt.PPID, evt.CMD)
}

var (
	// identify ppid in stat file
	ppidRegex, _ = regexp.Compile("\\d+ \\(.*\\) [[:alpha:]] (\\d+)")
	// hook for testing, directly use Lstat syscall as os.Lstat hides data in Sys member
	lstat = syscall.Lstat
	// hook for testing
	open = func(s string) (io.ReadCloser, error) {
		return os.Open(s)
	}
)

func NewPSScanner(ppid bool, cmdLength int, cgroupFilter string, userFilter []string, cmdFilter []string) *PSScanner {
	return &PSScanner{
		enablePpid:   ppid,
		eventCh:      nil,
		maxCmdLength: cmdLength,
		cgroupFilter: cgroupFilter,
		userFilter: userFilter,
		cmdFilter: cmdFilter,
	}
}

func (p *PSScanner) Run(triggerCh chan struct{}) (chan PSEvent, chan error) {
	eventCh := make(chan PSEvent, 100)
	p.eventCh = eventCh
	errCh := make(chan error)
	pl := make(procList)

	go func() {
		for {
			<-triggerCh
			pl.refresh(p)
		}
	}()
	return eventCh, errCh
}

func (p *PSScanner) processNewPid(pid int) {
    if p.cgroupFilter != "" {
        cgroup, _ := readFile(fmt.Sprintf("/proc/%d/cgroup", pid), 512)

        if strings.Contains(string(cgroup), p.cgroupFilter) {
            return
        }
		//fmt.Print(string(cgroup))
    }
	statInfo := syscall.Stat_t{}
	errStat := lstat(fmt.Sprintf("/proc/%d", pid), &statInfo)
	if len(p.userFilter) > 0{
		// Determine user and check if we need to filter this
		userLookup, errUserLookup := user.LookupId(strconv.FormatUint(uint64(statInfo.Uid), 10))
		if errUserLookup == nil {
			if slices.Contains(p.userFilter, userLookup.Username) {
				return
			}
		}
	}
	cmdLine, errCmdLine := readFile(fmt.Sprintf("/proc/%d/cmdline", pid), p.maxCmdLength)
	ppid, _ := p.getPpid(pid)

	cmd := "???" // process probably terminated
	if errCmdLine == nil {
		for i := 0; i < len(cmdLine); i++ {
			if cmdLine[i] == 0 {
				cmdLine[i] = 32
			}
		}
		cmd = string(cmdLine)
	}

	uid := -1
	if errStat == nil {
		uid = int(statInfo.Uid)
	}

	// filter cmd
	if len(p.cmdFilter) > 0 {
		for _, v := range p.cmdFilter {
			if strings.Contains(cmd, v) {
				return
			}
			// also filter out incomplete / possibly terminated commands
			if cmd == "???"{
				return
			}
			// also filter out empty commands
			if cmd == ""{
				return
			}
		}
	}

	p.eventCh <- PSEvent{UID: uid, PID: pid, PPID: ppid, CMD: cmd}
}

func (p *PSScanner) getPpid(pid int) (int, error) {
	if !p.enablePpid {
		return -1, nil
	}

	stat, err := readFile(fmt.Sprintf("/proc/%d/stat", pid), 512)
	if err != nil {
		return -1, err
	}

	if m := ppidRegex.FindStringSubmatch(string(stat)); m != nil {
		return strconv.Atoi(m[1])
	}
	return -1, errors.New("corrupt stat file")
}

// no nonsense file reading
func readFile(filename string, maxlen int) ([]byte, error) {
	file, err := open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	buffer := make([]byte, maxlen)
	n, err := file.Read(buffer)
	if err != io.EOF && err != nil {
		return nil, err
	}
	return buffer[:n], nil
}
