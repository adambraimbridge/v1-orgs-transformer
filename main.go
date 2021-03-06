package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"

	fthealth "github.com/Financial-Times/go-fthealth/v1_1"
	"github.com/Financial-Times/http-handlers-go/httphandlers"
	"github.com/Financial-Times/service-status-go/gtg"
	status "github.com/Financial-Times/service-status-go/httphandlers"
	"github.com/Financial-Times/tme-reader/tmereader"
	"github.com/gorilla/mux"
	"github.com/jawher/mow.cli"
	"github.com/rcrowley/go-metrics"
	"github.com/sethgrid/pester"
	log "github.com/sirupsen/logrus"
)

func main() {
	app := cli.App("v1-orgs-transformer", "A RESTful API for transforming TME Oranisations to UP json")
	username := app.String(cli.StringOpt{
		Name:   "tme-username",
		Value:  "",
		Desc:   "TME username used for http basic authentication",
		EnvVar: "TME_USERNAME",
	})
	password := app.String(cli.StringOpt{
		Name:   "tme-password",
		Value:  "",
		Desc:   "TME password used for http basic authentication",
		EnvVar: "TME_PASSWORD",
	})
	token := app.String(cli.StringOpt{
		Name:   "token",
		Value:  "",
		Desc:   "Token to be used for accessig TME",
		EnvVar: "TOKEN",
	})
	baseURL := app.String(cli.StringOpt{
		Name:   "base-url",
		Value:  "http://localhost:8080/transformers/organisations/",
		Desc:   "Base url",
		EnvVar: "BASE_URL",
	})
	tmeBaseURL := app.String(cli.StringOpt{
		Name:   "tme-base-url",
		Value:  "https://tme.ft.com",
		Desc:   "TME base url",
		EnvVar: "TME_BASE_URL",
	})
	port := app.Int(cli.IntOpt{
		Name:   "port",
		Value:  8080,
		Desc:   "Port to listen on",
		EnvVar: "PORT",
	})
	maxRecords := app.Int(cli.IntOpt{
		Name:   "maxRecords",
		Value:  int(10000),
		Desc:   "Maximum records to be queried to TME",
		EnvVar: "MAX_RECORDS",
	})
	batchSize := app.Int(cli.IntOpt{
		Name:   "batchSize",
		Value:  int(10),
		Desc:   "Number of requests to be executed in parallel to TME",
		EnvVar: "BATCH_SIZE",
	})
	cacheFileName := app.String(cli.StringOpt{
		Name:   "cache-file-name",
		Value:  "cache.db",
		Desc:   "Cache file name",
		EnvVar: "CACHE_FILE_NAME",
	})

	tmeTaxonomyName := "ON"

	app.Action = func() {
		client := getResilientClient()
		modelTransformer := new(orgTransformer)
		s := newOrgService(
			tmereader.NewTmeRepository(
				client,
				*tmeBaseURL,
				*username,
				*password,
				*token,
				*maxRecords,
				*batchSize,
				tmeTaxonomyName,
				&tmereader.AuthorityFiles{},
				modelTransformer),
			*baseURL,
			tmeTaxonomyName,
			*maxRecords,
			*cacheFileName)
		defer s.shutdown()
		handler := newOrgsHandler(s)
		servicesRouter := mux.NewRouter()
		servicesRouter.HandleFunc(status.PingPath, status.PingHandler)
		servicesRouter.HandleFunc(status.PingPathDW, status.PingHandler)
		servicesRouter.HandleFunc(status.BuildInfoPath, status.BuildInfoHandler)
		servicesRouter.HandleFunc(status.BuildInfoPathDW, status.BuildInfoHandler)

		healthCheck := fthealth.TimedHealthCheck{
			HealthCheck: fthealth.HealthCheck{
				SystemCode:  "v1-orgs-transformer",
				Name:        "V1 Org Transformer Healthchecks",
				Description: "Checks for the health of the service",
				Checks: []fthealth.Check{
					handler.HealthCheck(),
				},
			},
			Timeout: 10 * time.Second,
		}

		servicesRouter.HandleFunc("/__health", fthealth.Handler(healthCheck))
		g2gHandler := status.NewGoodToGoHandler(gtg.StatusChecker(handler.GTG))
		servicesRouter.HandleFunc(status.GTGPath, g2gHandler)

		servicesRouter.HandleFunc("/transformers/organisations/__count", handler.getOrgCount).Methods("GET")
		servicesRouter.HandleFunc("/transformers/organisations/__ids", handler.getOrgIds).Methods("GET")
		servicesRouter.HandleFunc("/transformers/organisations/__reload", handler.reloadOrgs).Methods("POST")

		servicesRouter.HandleFunc("/transformers/organisations/{uuid:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}}", handler.getOrgByUUID).Methods("GET")
		servicesRouter.HandleFunc("/transformers/organisations", handler.getOrgs).Methods("GET")

		var h http.Handler = servicesRouter
		h = httphandlers.TransactionAwareRequestLoggingHandler(log.StandardLogger(), h)
		h = httphandlers.HTTPMetricsHandler(metrics.DefaultRegistry, h)
		http.Handle("/", h)

		log.Printf("listening on %d", *port)
		err := http.ListenAndServe(fmt.Sprintf(":%d", *port), nil)
		if err != nil {
			log.Errorf("Error by listen and serve: %v", err.Error())
		}
	}
	app.Run(os.Args)
}

func getResilientClient() *pester.Client {
	tr := &http.Transport{
		MaxIdleConnsPerHost: 32,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		Dial: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).Dial,
	}
	c := &http.Client{
		Transport: tr,
		Timeout:   30 * time.Second,
	}
	client := pester.NewExtendedClient(c)
	client.Backoff = pester.ExponentialBackoff
	client.MaxRetries = 5
	client.Concurrency = 1

	return client
}
