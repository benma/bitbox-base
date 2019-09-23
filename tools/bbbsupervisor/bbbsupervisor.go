// Copyright 2019 Shift Cryptosecurity AG, Switzerland.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// BitBox Base Supervisor
// ----------------------
// Watches systemd logs (via journalctl) and queries Prometheus to detect potential issues and take action.
//
// Functionality to implement:
// * System
//   - temperature control: monitor bbbfancontrol and throttle CPU if needed
//   - disk space: monitor free space on rootfs and ssd, perform cleanup of temp & logs
//   - swap: detect issues with swap file, no memory left or "zram decompression failed", perform reboot
//
// * Middleware
//   - monitor service availability
//
// * Bitcoin Core
//   - monitor service availability
//   - perform backup tasks
//   - switch between IBD and normal operation mode (e.g. adjust dbcache)
//
// * c-lightning
//   - monitor service availability
//   - perform backup tasks (once possible)
//
// * electrs
//   - monitor service availability
//   - track initial sync and full database compaction, restart service if needed
//
// * NGINX, Grafana, ...
//   - monitor service availability
//

package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/tidwall/gjson"
)

type watcher interface {
	watch()
}

// Command line arguments
var (
	helpArg      = flag.Bool("help", false, "show help")
	redisAddrArg = flag.String("redis-addr", "localhost:6379", "redis connection address")
	redisPassArg = flag.String("redis-pass", "", "redis password")
	redisDbArg   = flag.Int("redis-db", 0, "redis database number")
	versionArg   = flag.Bool("version", false, "return program version")
)

// Redis Connection
var redisConn redis.Conn

const (
	helpText = `
Watches systemd logs (via journalctl) and queries Prometheus to detect potential issues and take action.

Command-line arguments: 
	--help
	--redis-addr    redis connection address  (default "localhost:6379")
	--redis-db      redis database number     (default 0)
	--redis-pass    redis password
  --version
`
)

// watcherEvent represents an event triggered by a watcher
// e.g. that bitcoin or electrs has fully synced, or a service is not reachable
type watcherEvent struct {
	unit    string  // unit represents systemd unit name, e.g. 'bitcoind'
	trigger trigger // trigger could be e.g. 'triggerElectrsNoBitcoindConnectivity' or 'triggerPrometheusBitcoindIBD'
	measure string  // measure is something that is measured by the prometheusWatcher
	value   float64 // value is the value that has been measured
}

// logWatcher watches systemd service logs.
type logWatcher struct {
	unit   string            // systemd unit to watch, e.g 'bitcoind'
	events chan watcherEvent // channel for passing service Events (e.g. a systemd log entry)
	errs   chan error        // channel for passing errors (e.g. stderr outputs)
}

// prometheusWatcher watches metrics exposed by a Prometheus server
type prometheusWatcher struct {
	unit       string            // unit is the systemd unit that the expression belongs to (e.g. 'bitcoind')
	expression string            // expression is the PQL expression to query for.
	server     string            // server is the address of the prometheus server to query from
	trigger    trigger           // trigger is the trigger to fire when a expression has been read by this watcher
	interval   time.Duration     // interval query interval
	events     chan watcherEvent // channel for passing service Events (e.g. a systemd log entry)
	errs       chan error        // channel for passing errors (e.g. stderr outputs)
}

// watchers represents several watcher objects.
type watchers []watcher

// errWriter implements io.Writer and writes all contents as error into the wrapped chan.
type errWriter struct{ errs chan error }

type eventWriter struct {
	events chan watcherEvent
	unit   string
}

// supervisorState implements a current state for the supervisor.
// the state values are filled over time
type supervisorState struct {
	triggerLastExecuted    map[trigger]int64 // implements a state (timestamps) when a trigger was fired (to mitigate trigger flooding)
	prometheusLastStateIBD float64           // implements a state for the last `bitcoin_ibd` measurement value (to detect switches ibd <-> no-ibd)
}

// trigger is something specific that can happen for a service
type trigger int

const versionNum = 0.1
const prometheusURL = "http://localhost:9090"

const (
	triggerElectrsFullySynced = 1 + iota
	triggerElectrsNoBitcoindConnectivity
	triggerMiddlewareNoBitcoindConnectivity
	triggerPrometheusBitcoindIBD
)

// Map of possible triggers. Mapped by their trigger to a trigger name
var triggerNames = map[trigger]string{
	triggerElectrsFullySynced:               "electrsFullySynced",
	triggerElectrsNoBitcoindConnectivity:    "electrsNoBitcoindConnectivity",
	triggerMiddlewareNoBitcoindConnectivity: "triggerMiddlewareNoBitcoindConnectivity",
	triggerPrometheusBitcoindIBD:            "prometheusBitcoindIBD",
}

// String returns a human readable value for a trigger
func (t trigger) String() string {
	if val, ok := triggerNames[t]; ok { // check if the trigger exists in the triggerNames map
		return val
	}
	return ""
}

// Write implements the io.Writer interface by sending the content as a parsed event through the event channel.
func (ew eventWriter) Write(p []byte) (int, error) {
	// sometimes multiple log lines are read as one
	logLines := strings.Split(strings.TrimSuffix(string(p), "\n"), "\n")
	for _, line := range logLines {
		event := parseEvent(line, ew.unit)
		if event != nil {
			ew.events <- *event
		}
	}

	return len(p), nil
}

// Write implements the io.Writer interface by sending the content as error through the error channel.
func (ew errWriter) Write(p []byte) (int, error) {
	ew.errs <- fmt.Errorf(string(p))
	return len(p), nil
}

// watch indefinitely watches/follows systemd logs for a specified unit.
// It passes any systemd log output on to the event channel.
// If there are errors running the journalctl command or if there is any
// output to stderr, the errors are passed on in the error channel `errs`.
func (lw logWatcher) watch() {
	systemdArgs := []string{
		"--since=now",
		"--quiet",
		"--follow",
		"--unit",
		lw.unit,
	}

	cmdAsString := "journalctl " + strings.Join(systemdArgs, " ")
	cmd := exec.Command("/bin/journalctl", systemdArgs...)

	eveWriter := eventWriter{lw.events, lw.unit}
	errWriter := errWriter{lw.errs}

	cmd.Stdout = eveWriter // stdout of journalctl is written into the events channel
	cmd.Stderr = errWriter // stderr of journalctl is written into the errs channel

	log.Printf("Watching journalctl for unit %s (%s)\n", lw.unit, cmdAsString)

	if err := cmd.Run(); err != nil {
		errWriter.Write([]byte(fmt.Sprintf("failed to start cmd: %v", err)))
	}
	errWriter.Write([]byte(fmt.Sprintf("command %v unexpectedly exited", cmdAsString)))
}

// watch implements watch interface by calling the watchHandler repeatedly.
func (pw prometheusWatcher) watch() {
	for {
		pw.watchHandler()
		<-time.After(pw.interval)
	}
}

//by querying and watching values from a Prometheus server
func (pw prometheusWatcher) watchHandler() {
	json, err := pw.queryJSON()
	if err != nil {
		pw.errs <- err
		return
	}

	measuredValue, err := pw.parsePrometheusResponseAsFloat(json)
	if err != nil {
		pw.errs <- err
		return
	}

	pw.events <- watcherEvent{unit: pw.unit, trigger: pw.trigger, measure: pw.expression, value: measuredValue}
}

// query queries prometheus with the specified expression and returns the JSON as a string
func (pw prometheusWatcher) queryJSON() (string, error) {

	client := http.Client{
		Timeout: 5 * time.Second,
	}

	httpResp, err := client.Get(pw.server + "/api/v1/query?query=" + pw.expression)
	if err != nil {
		return "", fmt.Errorf("Failed to fetch %q from prometheus server: %v", pw.expression, err)
	}
	defer httpResp.Body.Close()

	body, err := ioutil.ReadAll(httpResp.Body)
	if err != nil {
		return "", fmt.Errorf("Failed to read response body from prometheus request for %q: %v", pw.expression, err)
	}

	bodyAsString := string(body)

	// check if the response is valid json
	if !gjson.Valid(bodyAsString) {
		return "", fmt.Errorf("Prometheus request for %q returned invalid JSON: %v", pw.expression, bodyAsString)
	}

	return bodyAsString, nil
}

// parsePrometheusResponseAsFloat parses a promethues JSON response and returns a float
func (pw prometheusWatcher) parsePrometheusResponseAsFloat(json string) (float64, error) {

	// Check for a valid prometheus json response by checking:
	// - the `status` == success
	// - the list `data.result` having one and only one entry
	// - the value list `data.result[0].value` having exactly two entires
	// - there exists a response value for our expression `data.result[0].value[1]`

	status := gjson.Get(json, "status").String()
	if status != "success" {
		return -1, fmt.Errorf("prometheus request for %q returned non-success (%s): %v", pw.expression, status, json)
	}

	queryResult := gjson.Get(json, "data.result").Array()
	if len(queryResult) != 1 {
		return -1, fmt.Errorf("unexpectedly got %d results from prometheus request for %s: %s", len(queryResult), pw.expression, json)
	}

	firstResultValue := queryResult[0].Map()["value"].Array()
	if len(firstResultValue) != 2 {
		return -1, fmt.Errorf("unexpectedly got %d values from prometheus request for %s: %s", len(firstResultValue), pw.expression, json)
	}

	if firstResultValue[1].Exists() == false {
		return -1, fmt.Errorf("the result value for %s does not exist: %s", pw.expression, json)
	}

	measuredValue := firstResultValue[1].Float()
	return measuredValue, nil
}

// handleFlags parses command line arguments and handles them
func handleFlags() {
	if *versionArg || *helpArg {
		fmt.Printf("bbbsupervisor version %v\n", versionNum)
		if *helpArg {
			fmt.Println(helpText)
		}
		os.Exit(0)
	}
}

// setupWatchers sets up prometheusWatchers and logWatchers and returns them
func setupWatchers(events chan watcherEvent, errs chan error) (ws watchers) {
	return watchers{
		logWatcher{"bitcoind", events, errs},
		logWatcher{"lightningd", events, errs},
		logWatcher{"electrs", events, errs},
		logWatcher{"bbbmiddleware", events, errs},
		prometheusWatcher{unit: "bitcoind", expression: "bitcoin_ibd", server: prometheusURL, interval: 10 * time.Second, trigger: triggerPrometheusBitcoindIBD, events: events, errs: errs},
	}
}

// startWatchers starts a go routine for each watcher.
// these goroutines run indefinitely.
func startWatchers(ws watchers) {
	for _, watcher := range ws {
		go watcher.watch()
	}
}

func connectRedis() (r redis.Conn, err error) {
	if len(*redisPassArg) > 0 {
		r, err = redis.Dial("tcp", *redisAddrArg, redis.DialDatabase(*redisDbArg))
	} else {
		r, err = redis.Dial("tcp", *redisAddrArg, redis.DialPassword(*redisPassArg), redis.DialDatabase(*redisDbArg))
	}
	if err != nil {
		return nil, err
	}

	_, err = r.Do("PING")
	return r, err
}

func setRedisValue(key string, value string) (err error) {
	_, err = redisConn.Do("SET", key, value)
	return
}

func getRedisInt(key string) (value int, err error) {
	value, err = redis.Int(redisConn.Do("GET", key))
	return
}

func main() {
	flag.Parse()
	handleFlags()

	events := make(chan watcherEvent) // channel to process events a watcher detects
	errs := make(chan error)          // channel to process errors from watchers

	// initialize the initial and empty state
	state := supervisorState{
		triggerLastExecuted:    make(map[trigger]int64),
		prometheusLastStateIBD: -1,
	}

	var err error
	redisConn, err = connectRedis()
	if err != nil {
		log.Printf("Error: Could not connect to redis: %v\n", err)
	}

	ws := setupWatchers(events, errs)
	startWatchers(ws)
	eventLoop(events, errs, &state)
}
