// Copyright (C) 2018 spdfg
//
// This file is part of Elektron.
//
// Elektron is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Elektron is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with Elektron.  If not, see <http://www.gnu.org/licenses/>.
//

package powerCap

import (
	"bufio"
	"container/ring"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	elekLog "github.com/spdfg/elektron/logging"
	. "github.com/spdfg/elektron/logging/types"
	"github.com/spdfg/elektron/pcp"
	"github.com/spdfg/elektron/rapl"
)

func StartPCPLogAndExtremaDynamicCap(quit chan struct{}, logging *bool, hiThreshold, loThreshold float64, pcpConfigFile string) {

	var pcpCommand string = "pmdumptext -m -l -f '' -t 1.0 -d , -c " + pcpConfigFile
	cmd := exec.Command("sh", "-c", pcpCommand, pcpConfigFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if hiThreshold < loThreshold {
		elekLog.Log(CONSOLE, log.InfoLevel, "High threshold is lower than low threshold!")
	}

	pipe, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatal(err)
	}
	//cmd.Stdout = stdout

	scanner := bufio.NewScanner(pipe)

	go func(logging *bool, hiThreshold, loThreshold float64) {
		// Get names of the columns.
		scanner.Scan()

		// Write to logfile
		elekLog.Log(PCP, log.InfoLevel, scanner.Text())

		headers := strings.Split(scanner.Text(), ",")

		powerIndexes := make([]int, 0, 0)
		powerHistories := make(map[string]*ring.Ring)
		indexToHost := make(map[int]string)

		for i, hostMetric := range headers {
			metricSplit := strings.Split(hostMetric, ":")

			if strings.Contains(metricSplit[1], "RAPL_ENERGY_PKG") ||
				strings.Contains(metricSplit[1], "RAPL_ENERGY_DRAM") {
				powerIndexes = append(powerIndexes, i)
				indexToHost[i] = metricSplit[0]

				// Only create one ring per host.
				if _, ok := powerHistories[metricSplit[0]]; !ok {
					// Two PKGS, two DRAM per node, 20 - 5 seconds of tracking.
					powerHistories[metricSplit[0]] = ring.New(20)
				}
			}
		}

		// Throw away first set of results.
		scanner.Scan()

		cappedHosts := make(map[string]bool)
		orderCapped := make([]string, 0, 8)
		clusterPowerHist := ring.New(5)
		seconds := 0

		for scanner.Scan() {

			if *logging {

				elekLog.Log(CONSOLE, log.InfoLevel, "Logging PCP...")

				text := scanner.Text()
				split := strings.Split(text, ",")

				elekLog.Log(PCP, log.InfoLevel, text)

				totalPower := 0.0
				for _, powerIndex := range powerIndexes {
					power, _ := strconv.ParseFloat(split[powerIndex], 64)

					host := indexToHost[powerIndex]

					powerHistories[host].Value = power
					powerHistories[host] = powerHistories[host].Next()

					elekLog.WithFields(log.Fields{
						"Host":  indexToHost[powerIndex],
						"Power": fmt.Sprintf("%f", power*pcp.RAPLUnits),
					}).Log(CONSOLE, log.InfoLevel, "")

					totalPower += power
				}
				clusterPower := totalPower * pcp.RAPLUnits

				clusterPowerHist.Value = clusterPower
				clusterPowerHist = clusterPowerHist.Next()

				clusterMean := pcp.AverageClusterPowerHistory(clusterPowerHist)

				elekLog.WithFields(log.Fields{
					"Total power": fmt.Sprintf("%f %d Sec", clusterPower, clusterPowerHist.Len()),
					"Avg":         fmt.Sprintf("%f", clusterMean),
				}).Log(CONSOLE, log.InfoLevel, "")

				if clusterMean > hiThreshold {
					elekLog.Log(CONSOLE, log.InfoLevel, "Need to cap a node")
					// Create statics for all victims and choose one to cap
					victims := make([]pcp.Victim, 0, 8)

					// TODO: Just keep track of the largest to reduce fron nlogn to n
					for name, history := range powerHistories {

						histMean := pcp.AverageNodePowerHistory(history)

						// Consider doing mean calculations using go routines if we need to speed up.
						victims = append(victims, pcp.Victim{Watts: histMean, Host: name})
					}

					sort.Sort(pcp.VictimSorter(victims)) // Sort by average wattage.

					// From  best victim to worst, if everyone is already capped NOOP.
					for _, victim := range victims {
						// Only cap if host hasn't been capped yet.
						if !cappedHosts[victim.Host] {
							cappedHosts[victim.Host] = true
							orderCapped = append(orderCapped, victim.Host)
							elekLog.WithField("Avg. Wattage",
								fmt.Sprintf("%f", victim.Watts*pcp.RAPLUnits)).Logf(CONSOLE, log.InfoLevel, "Capping Victim %s", victim.Host)
							if err := rapl.Cap(victim.Host, "rapl", 50); err != nil {
								elekLog.Log(CONSOLE, log.ErrorLevel, "Error capping host")
							}
							break // Only cap one machine at at time.
						}
					}

				} else if clusterMean < loThreshold {

					if len(orderCapped) > 0 {
						host := orderCapped[len(orderCapped)-1]
						orderCapped = orderCapped[:len(orderCapped)-1]
						cappedHosts[host] = false
						// User RAPL package to send uncap.
						elekLog.Logf(CONSOLE, log.InfoLevel, "Uncapping host %s", host)
						if err := rapl.Cap(host, "rapl", 100); err != nil {
							elekLog.Log(CONSOLE, log.ErrorLevel, "Error capping host")
						}
					}
				}
			}

			seconds++
		}
	}(logging, hiThreshold, loThreshold)

	elekLog.Log(CONSOLE, log.InfoLevel, "PCP logging started")

	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}

	pgid, err := syscall.Getpgid(cmd.Process.Pid)

	select {
	case <-quit:
		elekLog.Log(CONSOLE, log.InfoLevel, "Stopping PCP logging in 5 seconds")
		time.Sleep(5 * time.Second)

		// http://stackoverflow.com/questions/22470193/why-wont-go-kill-a-child-process-correctly
		// Kill process and all children processes.
		syscall.Kill(-pgid, 15)
		return
	}
}
