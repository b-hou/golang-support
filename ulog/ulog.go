package ulog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/syslog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	TIME_NONE int = iota
	TIME_DATE
	TIME_TIMESTAMP
)

var facilities = map[string]syslog.Priority{
	"user":   syslog.LOG_USER,
	"daemon": syslog.LOG_DAEMON,
	"local0": syslog.LOG_LOCAL0,
	"local1": syslog.LOG_LOCAL1,
	"local2": syslog.LOG_LOCAL2,
	"local3": syslog.LOG_LOCAL3,
	"local4": syslog.LOG_LOCAL4,
	"local5": syslog.LOG_LOCAL5,
	"local6": syslog.LOG_LOCAL6,
	"local7": syslog.LOG_LOCAL7,
}
var severities = map[string]syslog.Priority{
	"error":   syslog.LOG_ERR,
	"warning": syslog.LOG_WARNING,
	"info":    syslog.LOG_INFO,
	"debug":   syslog.LOG_DEBUG,
}

var severityLabels = map[syslog.Priority]string{
	syslog.LOG_ERR:     "ERRO ",
	syslog.LOG_WARNING: "WARN ",
	syslog.LOG_INFO:    "INFO ",
	syslog.LOG_DEBUG:   "DBUG ",
}

var severityColors = map[syslog.Priority]string{
	syslog.LOG_ERR:     "\x1b[31m",
	syslog.LOG_WARNING: "\x1b[33m",
	syslog.LOG_INFO:    "\x1b[36m",
	syslog.LOG_DEBUG:   "\x1b[32m",
}

type ULog struct {
	file, console, syslog bool
	fileHandle            *os.File
	filePath              string
	filePreviousPath      string
	fileTime              int
	fileSeverity          bool
	consoleHandle         io.Writer
	consoleTime           int
	consoleSeverity       bool
	consoleColors         bool
	consoleColorizer      *regexp.Regexp
	syslogHandle          *syslog.Writer
	syslogRemote          string
	syslogName            string
	syslogFacility        syslog.Priority
	optionUTC             bool
	lastCheck             time.Time
	level                 syslog.Priority
	sync.Mutex
}

func New(target string) *ULog {
	log := &ULog{
		fileHandle:   nil,
		syslogHandle: nil,
	}
	return log.Load(target)
}

func (this *ULog) Load(target string) *ULog {
	this.Close()
	this.Lock()
	defer this.Unlock()

	this.file = false
	this.filePath = ""
	this.filePreviousPath = ""
	this.fileTime = TIME_DATE
	this.fileSeverity = true
	this.console = false
	this.consoleTime = TIME_DATE
	this.consoleSeverity = true
	this.consoleColors = true
	this.consoleColorizer = regexp.MustCompile(`"([^"]+)"\s*:`)
	this.consoleHandle = os.Stderr
	this.syslog = false
	this.syslogRemote = ""
	this.syslogName = filepath.Base(os.Args[0])
	this.syslogFacility = syslog.LOG_DAEMON
	this.optionUTC = false
	this.lastCheck = time.Unix(0, 0)
	this.level = syslog.LOG_INFO
	for _, target := range regexp.MustCompile("(file|console|syslog|option)\\s*\\(([^\\)]*)\\)").FindAllStringSubmatch(target, -1) {
		switch strings.ToLower(target[1]) {
		case "file":
			this.file = true
			for _, option := range regexp.MustCompile("([^:=,\\s]+)\\s*[:=]\\s*([^,\\s]+)").FindAllStringSubmatch(target[2], -1) {
				switch strings.ToLower(option[1]) {
				case "path":
					this.filePath = option[2]
				case "time":
					option[2] = strings.ToLower(option[2])
					switch {
					case option[2] == "stamp" || option[2] == "timestamp":
						this.fileTime = TIME_TIMESTAMP
					case option[2] != "1" && option[2] != "true" && option[2] != "on" && option[2] != "yes":
						this.fileTime = TIME_NONE
					}
				case "severity":
					option[2] = strings.ToLower(option[2])
					if option[2] != "1" && option[2] != "true" && option[2] != "on" && option[2] != "yes" {
						this.fileSeverity = false
					}
				}
			}
			if this.filePath == "" {
				this.file = false
			}
		case "console":
			this.console = true
			for _, option := range regexp.MustCompile("([^:=,\\s]+)\\s*[:=]\\s*([^,\\s]+)").FindAllStringSubmatch(target[2], -1) {
				option[2] = strings.ToLower(option[2])
				switch strings.ToLower(option[1]) {
				case "output":
					if option[2] == "stdout" {
						this.consoleHandle = os.Stdout
					}
				case "time":
					switch {
					case option[2] == "stamp" || option[2] == "timestamp":
						this.consoleTime = TIME_TIMESTAMP
					case option[2] != "1" && option[2] != "true" && option[2] != "on" && option[2] != "yes":
						this.consoleTime = TIME_NONE
					}
				case "severity":
					if option[2] != "1" && option[2] != "true" && option[2] != "on" && option[2] != "yes" {
						this.consoleSeverity = false
					}
				case "colors":
					if option[2] != "1" && option[2] != "true" && option[2] != "on" && option[2] != "yes" {
						this.consoleColors = false
					}
				}
			}
		case "syslog":
			this.syslog = true
			for _, option := range regexp.MustCompile("([^:=,\\s]+)\\s*[:=]\\s*([^,\\s]+)").FindAllStringSubmatch(target[2], -1) {
				switch strings.ToLower(option[1]) {
				case "remote":
					this.syslogRemote = option[2]
					if !regexp.MustCompile(":\\d+$").MatchString(this.syslogRemote) {
						this.syslogRemote += ":514"
					}
				case "name":
					this.syslogName = option[2]
				case "facility":
					this.syslogFacility = facilities[strings.ToLower(option[2])]
				}
			}
		case "option":
			for _, option := range regexp.MustCompile("([^:=,\\s]+)\\s*[:=]\\s*([^,\\s]+)").FindAllStringSubmatch(target[2], -1) {
				option[2] = strings.ToLower(option[2])
				switch strings.ToLower(option[1]) {
				case "utc":
					if option[2] == "1" || option[2] == "true" || option[2] == "on" || option[2] == "yes" {
						this.optionUTC = true
					}
				case "level":
					this.level = severities[strings.ToLower(option[2])]
				}
			}
		}
	}
	return this
}

func (this *ULog) Close() {
	this.Lock()
	defer this.Unlock()
	if this.syslogHandle != nil {
		this.syslogHandle.Close()
		this.syslogHandle = nil
	}
	if this.fileHandle != nil {
		this.fileHandle.Close()
		this.fileHandle = nil
	}
}

func (this *ULog) SetLevel(level string) {
	level = strings.ToLower(level)
	switch level {
	case "error":
		this.level = syslog.LOG_ERR
	case "warning":
		this.level = syslog.LOG_WARNING
	case "info":
		this.level = syslog.LOG_INFO
	case "debug":
		this.level = syslog.LOG_DEBUG
	}
}

func strftime(layout string, base time.Time) string {
	var output []string

	length := len(layout)
	for index := 0; index < length; index++ {
		switch layout[index] {
		case '%':
			if index < length-1 {
				switch layout[index+1] {
				case 'a':
					output = append(output, base.Format("Mon"))
				case 'A':
					output = append(output, base.Format("Monday"))
				case 'b':
					output = append(output, base.Format("Jan"))
				case 'B':
					output = append(output, base.Format("January"))
				case 'c':
					output = append(output, base.Format("Mon Jan 2 15:04:05 2006"))
				case 'C':
					output = append(output, fmt.Sprintf("%02d", base.Year()/100))
				case 'd':
					output = append(output, fmt.Sprintf("%02d", base.Day()))
				case 'D':
					output = append(output, fmt.Sprintf("%02d/%02d/%02d", base.Month(), base.Day(), base.Year()%100))
				case 'e':
					output = append(output, fmt.Sprintf("%2d", base.Day()))
				case 'f':
					output = append(output, fmt.Sprintf("%06d", base.Nanosecond()/1000))
				case 'F':
					output = append(output, fmt.Sprintf("%04d-%02d-%02d", base.Year(), base.Month(), base.Day()))
				case 'g':
					year, _ := base.ISOWeek()
					output = append(output, fmt.Sprintf("%02d", year%100))
				case 'G':
					year, _ := base.ISOWeek()
					output = append(output, fmt.Sprintf("%04d", year))
				case 'h':
					output = append(output, base.Format("Jan"))
				case 'H':
					output = append(output, fmt.Sprintf("%02d", base.Hour()))
				case 'I':
					if base.Hour() == 0 || base.Hour() == 12 {
						output = append(output, "12")
					} else {
						output = append(output, fmt.Sprintf("%02d", base.Hour()%12))
					}
				case 'j':
					output = append(output, fmt.Sprintf("%03d", base.YearDay()))
				case 'k':
					output = append(output, fmt.Sprintf("%2d", base.Hour()))
				case 'l':
					if base.Hour() == 0 || base.Hour() == 12 {
						output = append(output, "12")
					} else {
						output = append(output, fmt.Sprintf("%2d", base.Hour()%12))
					}
				case 'm':
					output = append(output, fmt.Sprintf("%02d", base.Month()))
				case 'M':
					output = append(output, fmt.Sprintf("%02d", base.Minute()))
				case 'n':
					output = append(output, "\n")
				case 'p':
					if base.Hour() < 12 {
						output = append(output, "AM")
					} else {
						output = append(output, "PM")
					}
				case 'P':
					if base.Hour() < 12 {
						output = append(output, "am")
					} else {
						output = append(output, "pm")
					}
				case 'r':
					if base.Hour() == 0 || base.Hour() == 12 {
						output = append(output, "12")
					} else {
						output = append(output, fmt.Sprintf("%02d", base.Hour()%12))
					}
					output = append(output, fmt.Sprintf(":%02d:%02d", base.Minute(), base.Second()))
					if base.Hour() < 12 {
						output = append(output, " AM")
					} else {
						output = append(output, " PM")
					}
				case 'R':
					output = append(output, fmt.Sprintf("%02d:%02d", base.Hour(), base.Minute()))
				case 's':
					output = append(output, fmt.Sprintf("%d", base.Unix()))
				case 'S':
					output = append(output, fmt.Sprintf("%02d", base.Second()))
				case 't':
					output = append(output, "\t")
				case 'T':
					output = append(output, fmt.Sprintf("%02d:%02d:%02d", base.Hour(), base.Minute(), base.Second()))
				case 'u':
					day := base.Weekday()
					if day == 0 {
						day = 7
					}
					output = append(output, fmt.Sprintf("%d", day))
				case 'U':
					output = append(output, fmt.Sprintf("%d", (base.YearDay()+6-int(base.Weekday()))/7))
				case 'V':
					_, week := base.ISOWeek()
					output = append(output, fmt.Sprintf("%02d", week))
				case 'w':
					output = append(output, fmt.Sprintf("%d", base.Weekday()))
				case 'W':
					day := int(base.Weekday())
					if day == 0 {
						day = 6
					} else {
						day -= 1
					}
					output = append(output, fmt.Sprintf("%d", (base.YearDay()+6-day)/7))
				case 'x':
					output = append(output, fmt.Sprintf("%02d/%02d/%02d", base.Month(), base.Day(), base.Year()%100))
				case 'X':
					output = append(output, fmt.Sprintf("%02d:%02d:%02d", base.Hour(), base.Minute(), base.Second()))
				case 'y':
					output = append(output, fmt.Sprintf("%02d", base.Year()%100))
				case 'Y':
					output = append(output, fmt.Sprintf("%04d", base.Year()))
				case 'z':
					output = append(output, base.Format("-0700"))
				case 'Z':
					output = append(output, base.Format("MST"))
				case '%':
					output = append(output, "%")
				}
				index++
			}
		default:
			output = append(output, string(layout[index]))
		}
	}
	return strings.Join(output, "")
}

func (this *ULog) log(severity syslog.Priority, xlayout interface{}, a ...interface{}) {
	var err error
	if this.level < severity || (!this.syslog && !this.file && !this.console) {
		return
	}
	layout := ""
	switch reflect.TypeOf(xlayout).Kind() {
	case reflect.Map:
		var buffer bytes.Buffer

		encoder := json.NewEncoder(&buffer)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(xlayout); err == nil {
			layout = "%s"
			a = []interface{}{bytes.TrimSpace(buffer.Bytes())}
		}
	case reflect.String:
		layout = xlayout.(string)
	}
	layout = strings.TrimSpace(layout)
	if this.syslog {
		if this.syslogHandle == nil {
			this.Lock()
			if this.syslogHandle == nil {
				protocol := ""
				if this.syslogRemote != "" {
					protocol = "udp"
				}
				if this.syslogHandle, err = syslog.Dial(protocol, this.syslogRemote, this.syslogFacility, this.syslogName); err != nil {
					this.syslogHandle = nil
				}
			}
			this.Unlock()
		}
		if this.syslogHandle != nil {
			switch severity {
			case syslog.LOG_ERR:
				this.syslogHandle.Err(fmt.Sprintf(layout, a...))
			case syslog.LOG_WARNING:
				this.syslogHandle.Warning(fmt.Sprintf(layout, a...))
			case syslog.LOG_INFO:
				this.syslogHandle.Info(fmt.Sprintf(layout, a...))
			case syslog.LOG_DEBUG:
				this.syslogHandle.Debug(fmt.Sprintf(layout, a...))
			}
		}
	}
	now := time.Now()
	if this.optionUTC {
		now = now.UTC()
	} else {
		now = now.Local()
	}
	if this.file {
		if now.Sub(this.lastCheck) >= time.Second || this.fileHandle == nil {
			this.lastCheck = now
			this.Lock()
			path := strftime(this.filePath, now)
			if path != this.filePreviousPath {
				if this.fileHandle != nil {
					this.fileHandle.Close()
					this.fileHandle = nil
				}
				this.filePreviousPath = path
			}
			if this.fileHandle == nil {
				os.MkdirAll(filepath.Dir(path), 0755)
				if this.fileHandle, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err != nil {
					this.fileHandle = nil
				}
			}
			this.Unlock()
		}
		if this.fileHandle != nil {
			prefix := ""
			switch this.fileTime {
			case TIME_DATE:
				prefix = fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d ", now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second())
			case TIME_TIMESTAMP:
				prefix = fmt.Sprintf("%d ", now.Unix())
			}
			if this.fileSeverity {
				prefix += severityLabels[severity]
			}
			this.Lock()
			this.fileHandle.WriteString(fmt.Sprintf(prefix+layout+"\n", a...))
			this.Unlock()
		}
	}
	if this.console {
		prefix := ""
		switch this.consoleTime {
		case TIME_DATE:
			prefix = fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d ", now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second())
		case TIME_TIMESTAMP:
			prefix = fmt.Sprintf("%d ", now.Unix())
		}
		if this.consoleSeverity {
			if this.consoleColors {
				prefix += fmt.Sprintf("%s%s\x1b[0m", severityColors[severity], severityLabels[severity])
			} else {
				prefix += severityLabels[severity]
			}
		}
		if reflect.TypeOf(xlayout).Kind() == reflect.Map && this.consoleColors {
			for index, _ := range a {
				a[index] = this.consoleColorizer.ReplaceAllString(fmt.Sprintf("%s", a[index]), "\"\x1b[37m$1\x1b[0m\":")
			}
		}
		this.Lock()
		fmt.Fprintf(this.consoleHandle, prefix+layout+"\n", a...)
		this.Unlock()
	}
}

func (this *ULog) Error(layout interface{}, a ...interface{}) {
	this.log(syslog.LOG_ERR, layout, a...)
}
func (this *ULog) Warn(layout interface{}, a ...interface{}) {
	this.log(syslog.LOG_WARNING, layout, a...)
}
func (this *ULog) Info(layout interface{}, a ...interface{}) {
	this.log(syslog.LOG_INFO, layout, a...)
}
func (this *ULog) Debug(layout interface{}, a ...interface{}) {
	this.log(syslog.LOG_DEBUG, layout, a...)
}
