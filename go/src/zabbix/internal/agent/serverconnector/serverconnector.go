/*
** Zabbix
** Copyright (C) 2001-2019 Zabbix SIA
**
** This program is free software; you can redistribute it and/or modify
** it under the terms of the GNU General Public License as published by
** the Free Software Foundation; either version 2 of the License, or
** (at your option) any later version.
**
** This program is distributed in the hope that it will be useful,
** but WITHOUT ANY WARRANTY; without even the implied warranty of
** MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
** GNU General Public License for more detailm.
**
** You should have received a copy of the GNU General Public License
** along with this program; if not, write to the Free Software
** Foundation, Inc., 51 Franklin Street, Fifth Floor, Boston, MA  02110-1301, USA.
**/

package serverconnector

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
	"zabbix/internal/agent"
	"zabbix/internal/agent/resultcache"
	"zabbix/internal/agent/scheduler"
	"zabbix/internal/monitor"
	"zabbix/internal/plugin"
	"zabbix/pkg/comms"
	"zabbix/pkg/log"
)

type Connector struct {
	clientID    uint64
	input       chan interface{}
	address     string
	lastError   error
	resultCache *resultcache.ResultCache
	taskManager scheduler.Scheduler
}

type activeChecksRequest struct {
	Request string `json:"request"`
	Host    string `json:"host"`
}

type activeChecksResponse struct {
	Response string            `json:"response"`
	Info     string            `json:"info"`
	Data     []*plugin.Request `json:"data"`
}

type agendDataResponse struct {
	Response string `json:"response"`
	Info     string `json:"info"`
}

func ParseServerActive() ([]string, error) {
	addresses := strings.Split(agent.Options.ServerActive, ",")

	for i := 0; i < len(addresses); i++ {
		if strings.IndexByte(addresses[i], ':') == -1 {
			if _, _, err := net.SplitHostPort(addresses[i] + ":10051"); err != nil {
				return nil, fmt.Errorf("error parsing the \"ServerActive\" parameter: address \"%s\": %s", addresses[i], err)
			}
			addresses[i] += ":10051"
		} else {
			if _, _, err := net.SplitHostPort(addresses[i] + ":10051"); err != nil {
				return nil, fmt.Errorf("error parsing the \"ServerActive\" parameter: address \"%s\": %s", addresses[i], err)
			}
		}

		for j := 0; j < i; j++ {
			if addresses[j] == addresses[i] {
				return nil, fmt.Errorf("error parsing the \"ServerActive\" parameter: address \"%s\" specified more than once", addresses[i])
			}
		}
	}

	return addresses, nil
}

func (c *Connector) Addr() (s string) {
	return c.address
}

func (c *Connector) refreshActiveChecks() {
	var err error

	log.Debugf("[%d] In refreshActiveChecks() from [%s]", c.clientID, c.address)
	defer log.Debugf("[%d] End of refreshActiveChecks() from [%s]", c.clientID, c.address)

	request, err := json.Marshal(&activeChecksRequest{Request: "active checks", Host: agent.Options.Hostname})
	if err != nil {
		log.Errf("[%d] cannot create active checks request to [%s]: %s", c.clientID, c.address, err)
		return
	}

	data, err := comms.Exchange(c.address, time.Second*time.Duration(agent.Options.Timeout), request)

	if err != nil {
		if c.lastError == nil || err.Error() != c.lastError.Error() {
			log.Warningf("[%d] active check configuration update from [%s] started to fail (%s)", c.clientID,
				c.address, err)
			c.lastError = err
		}
		return
	}

	if c.lastError != nil {
		log.Warningf("[%d] active check configuration update from [%s] is working again", c.clientID, c.address)
		c.lastError = nil
	}

	var response activeChecksResponse

	err = json.Unmarshal(data, &response)
	if err != nil {
		log.Errf("[%d] cannot parse list of active checks from [%s]: %s", c.clientID, c.address, err)
		return
	}

	if response.Response != "success" {
		if len(response.Info) != 0 {
			log.Errf("[%d] no active checks on server [%s]: %s", c.clientID, c.address, response.Info)
		} else {
			log.Errf("[%d] no active checks on server [%s]", c.clientID, c.address)
		}

		return
	}

	if nil == response.Data {
		log.Errf("[%d] cannot parse list of active checks: data array is missing", c.clientID)
		return
	}

	c.taskManager.UpdateTasks(c.clientID, c.resultCache, response.Data)
}

func (c *Connector) Write(data []byte) (n int, err error) {
	b, err := comms.Exchange(c.address, time.Second*time.Duration(agent.Options.Timeout), data)
	if err != nil {
		return 0, err
	}

	var response agendDataResponse

	err = json.Unmarshal(b, &response)
	if err != nil {
		return 0, err
	}

	if response.Response != "success" {
		if len(response.Info) != 0 {
			return 0, fmt.Errorf("%s", response.Info)
		}
		return 0, errors.New("unsuccessful response")
	}

	return len(data), nil
}

func (c *Connector) run() {
	var start time.Time

	defer log.PanicHook()
	log.Debugf("[%d] starting server connector for '%s'", c.clientID, c.address)

	ticker := time.NewTicker(time.Second)
run:
	for {
		select {
		case <-ticker.C:
			c.resultCache.Flush()
			if time.Since(start) > time.Duration(agent.Options.RefreshActiveChecks)*time.Second {
				c.refreshActiveChecks()
				start = time.Now()
			}
		case <-c.input:
			break run
		}
	}
	close(c.input)
	log.Debugf("[%d] server connector has been stopped", c.clientID)
	monitor.Unregister()
}

func New(taskManager *scheduler.Manager, address string) *Connector {
	c := &Connector{
		taskManager: taskManager,
		address:     address,
		input:       make(chan interface{}, 10),
		clientID:    agent.NewClientID(),
	}
	c.resultCache = resultcache.NewActive(c.clientID, c)

	return c
}

func (c *Connector) Start() {
	c.resultCache.Start()
	monitor.Register()
	go c.run()
}

func (c *Connector) Stop() {
	c.input <- nil
	c.resultCache.Stop()
}
