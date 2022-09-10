package main

import (
	"encoding/json"
	"flag"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gjolly/go-rmadison/pkg/debian"
	"github.com/go-resty/resty/v2"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

var log *zap.SugaredLogger

func init() {
	// Logger for the operations
	logger, _ := zap.NewDevelopment()
	log = logger.Sugar()
}

type httpHandler struct {
	Caches []*debian.Archive
}

func (h httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	pkg := strings.TrimLeft(r.URL.Path, "/")
	log.Debugf("lookup for %v", pkg)

	if strings.Contains(pkg, "/") {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	allInfo := make(map[string]*debian.PackageInfo, 0)
	for _, cache := range h.Caches {
		allInfoArchive, ok := cache.Packages[pkg]
		if ok {
			for k, v := range allInfoArchive {
				allInfo[k] = v
			}
		}
	}

	// convert the dictionary to a list
	fmtInfo := make([]*debian.PackageInfo, len(allInfo))
	i := 0
	for _, info := range allInfo {
		fmtInfo[i] = info
		i++
	}

	jsonInfo, err := json.Marshal(fmtInfo)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Add("Content-Type", "application/json")
	w.Write(jsonInfo)
}

func refreshCaches(archives []*debian.Archive) {
	for _, cache := range archives {
		go func(cache *debian.Archive) {
			t := time.NewTicker(5 * time.Minute)
			for {
				now := time.Now()
				_, pkgStats, err := cache.RefreshCache(false)
				duration := time.Now().Sub(now)
				if err != nil {
					log.Errorf("cache refreshed in %v (with error %v), %v packages updated", duration.Seconds(), err, pkgStats)
				} else {
					log.Infof("cache refreshed in %v, %v packages updated", duration.Seconds(), pkgStats)
				}

				<-t.C
			}
		}(cache)
	}
}

// Config is the configuration of the rmadison server
type Config struct {
	Caches []*debian.Archive
}

type archiveYAMLConf struct {
	BaseURL  string   `yaml:"base_url"`
	PortsURL string   `yaml:"ports_url"`
	Pockets  []string `yaml:"pockets"`
}

func parseConfig(configPath string) (*Config, error) {
	configFile, err := os.Open(configPath)
	if err != nil {
		return nil, err
	}

	configBytes, err := ioutil.ReadAll(configFile)
	if err != nil {
		return nil, err
	}
	rawConfig := new(struct {
		CacheDirectory string             `yaml:"cache_directory"`
		Archives       []*archiveYAMLConf `yaml:"archives"`
	})
	yaml.Unmarshal(configBytes, rawConfig)
	conf := new(Config)
	conf.Caches = make([]*debian.Archive, len(rawConfig.Archives))

	httpClient := resty.New()

	for i, archiveConf := range rawConfig.Archives {
		baseURL, err := url.Parse(archiveConf.BaseURL)
		if err != nil {
			return nil, err
		}
		portsURL, err := url.Parse(archiveConf.PortsURL)
		if err != nil {
			return nil, err
		}
		conf.Caches[i] = &debian.Archive{
			BaseURL:  baseURL,
			PortsURL: portsURL,
			Pockets:  archiveConf.Pockets,
			CacheDir: rawConfig.CacheDirectory,
			Client:   httpClient,
		}
	}

	return conf, err
}

func main() {
	flag.Parse()
	cacheDir := flag.Arg(0)
	if cacheDir == "" {
		cacheDir, _ = os.MkdirTemp("", "gormadisontest")
	}

	conf, err := parseConfig("config.yaml")
	if err != nil {
		log.Fatalf("failed to read config file: %v", err)
	}

	if len(conf.Caches) == 0 {
		log.Fatal("No archive defined in config file")
	}

	log.Info("Reading local cache")
	for _, cache := range conf.Caches {
		_, packages, err := cache.RefreshCache(true)
		if err != nil {
			log.Error("error reading existing cache data:", err)
		}
		log.Infof("packages in cache: %v", packages)
	}

	refreshCaches(conf.Caches)
	handler := httpHandler{
		Caches: conf.Caches,
	}
	addr := ":8433"
	s := &http.Server{
		Addr:           addr,
		Handler:        handler,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	log.Infof("starting http server on %v\n", addr)
	log.Fatal(s.ListenAndServe())
}
