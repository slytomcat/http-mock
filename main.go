package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"

	"github.com/spf13/cobra"
)

type filter struct {
	re      *regexp.Regexp
	path    string
	code    int
	headers map[string]string
	stream  bool
}

type cfgValue struct {
	Re      string            `json:"re"`
	Path    string            `json:"path"`
	Code    int               `json:"code,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Stream  bool              `json:"stream,omitempty"`
}

var (
	version      = "local build"
	forvardURL   string
	forvardHost  string
	config       string
	host         string
	port         int
	printVersion bool
	reURL        = regexp.MustCompile(`https?://([^/]+).*`)
	filters      []filter
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "http-mock -c <config file path> | -f <URL for request forwarding>",
		Short:   fmt.Sprintf("http-mock is proxy/mock service v. %s", version),
		Run:     root,
		Example: "http-mock -c config.json -p 8000\nor\nhttp-mock -f http://examle.com",
	}
	rootCmd.Flags().StringVarP(&host, "host", "s", "localhost", "host to start service")
	rootCmd.Flags().IntVarP(&port, "port", "p", 8080, "port to strat service")
	rootCmd.Flags().StringVarP(&forvardURL, "forward", "f", "", "URL for forwarding requests")
	rootCmd.Flags().StringVarP(&config, "config", "c", "", "path for configuration file")
	rootCmd.Flags().BoolVarP(&printVersion, "version", "v", false, "print version and exit")
	rootCmd.Execute()
}

func readConfig(path string) error {
	cfgData, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config file opening/reading error: %w", err)
	}
	cfg := make([]cfgValue, 0, 8)
	json.Unmarshal(cfgData, &cfg)
	for i, c := range cfg {
		if c.Re == "" || c.Path == "" {
			return fmt.Errorf("record #%d doesn't contain mandatory values in fields 'path' and 're'", i)
		}
		compiled, err := regexp.Compile(c.Re)
		if err != nil {
			return fmt.Errorf("compiling 're' field in record #%d error: %w", i, err)
		}
		if _, err = os.Stat(c.Path); os.IsNotExist(err) {
			return fmt.Errorf("file '%s' from 'path' of record #%d is not exists", c.Path, i)
		}
		filters = append(filters, filter{
			re:      compiled,
			path:    c.Path,
			code:    c.Code,
			stream:  c.Stream,
			headers: c.Headers,
		})
	}
	return nil
}

func root(cmd *cobra.Command, args []string) {
	if printVersion {
		fmt.Printf("http-mock v. %s\n", version)
		os.Exit(0)
	}
	switch {
	case forvardURL != "":
		if hosts := reURL.FindStringSubmatch(forvardURL); len(hosts) == 2 {
			forvardHost = hosts[1]
		} else {
			fmt.Printf("Error: incorrect URL: %s\n", forvardURL)
			os.Exit(1)
		}
		runProxy()
	case config != "":
		if err := readConfig(config); err != nil {
			fmt.Printf("Error: config loading error: %s\n", err)
			os.Exit(1)
		}
		runMOCK()
	default:
		fmt.Println("Error: ether -c or -f option have to be provided")
		os.Exit(1)
	}
}

func runProxy() {
	fmt.Printf("Starting proxy sevice for %s on %s:%d\n", forvardURL, host, port)
}

func runMOCK() {
	fmt.Printf("Starting MOCK server on %s:%d\n", host, port)
}
