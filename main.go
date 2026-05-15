package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/bytedance/sonic"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/valyala/fasthttp"
)

type project struct {
	ID                int    `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
	SSHURLToRepo      string `json:"ssh_url_to_repo"`
	Visibility        string `json:"visibility"`
}

var (
	flagURL     = flag.String("url", "", "gitlab base url, e.g. https://gitlab.example.com/")
	flagToken   = flag.String("token", "", "private token (PRIVATE-TOKEN header), optional for public instances")
	flagOutput  = flag.String("output", "repos", "output directory")
	flagWorkers = flag.Int("workers", 4, "parallel clone workers")
	flagSSH     = flag.Bool("ssh", false, "clone via ssh instead of https (requires ssh key setup)")
)

func fetchProjects(baseURL, token string) ([]project, error) {
	client := &fasthttp.Client{
		MaxResponseBodySize: 64 * 1024 * 1024,
	}

	var all []project
	page := 1

	for {
		url := fmt.Sprintf("%s/api/v4/projects?page=%d&per_page=100&archived=false", baseURL, page)

		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		req.SetRequestURI(url)
		req.Header.SetMethod(fasthttp.MethodGet)
		if token != "" {
			req.Header.Set("PRIVATE-TOKEN", token)
		}

		if err := client.Do(req, resp); err != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			return nil, fmt.Errorf("request page %d: %w", page, err)
		}

		if resp.StatusCode() != fasthttp.StatusOK {
			body := string(resp.Body())
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			return nil, fmt.Errorf("page %d: status %d: %s", page, resp.StatusCode(), body)
		}

		var batch []project
		if err := sonic.Unmarshal(resp.Body(), &batch); err != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			return nil, fmt.Errorf("unmarshal page %d: %w", page, err)
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		all = append(all, batch...)
		slog.Info("fetched page", "page", page, "count", len(batch), "total", len(all))

		if len(batch) < 100 {
			break
		}
		page++
	}

	return all, nil
}

func cloneProject(p project, outputDir, token string, useSSH bool) error {
	cloneURL := p.HTTPURLToRepo
	if useSSH {
		cloneURL = p.SSHURLToRepo
	}

	/* group/subgroup/repo layout is preserved via PathWithNamespace, e.g.: *
	 * mygroup/mysubgroup/myrepo -> outputDir/mygroup/mysubgroup/myrepo    */
	dest := filepath.Join(outputDir, p.PathWithNamespace)

	var auth *http.BasicAuth
	if token != "" && !useSSH {
		auth = &http.BasicAuth{Username: "oauth2", Password: token}
	}

	if _, err := os.Stat(filepath.Join(dest, "HEAD")); err == nil {
		slog.Info("already exists, fetching", "path", p.PathWithNamespace)
		repo, err := gogit.PlainOpen(dest)
		if err != nil {
			return fmt.Errorf("open: %w", err)
		}
		err = repo.Fetch(&gogit.FetchOptions{Auth: auth, Tags: gogit.AllTags})
		if errors.Is(err, gogit.NoErrAlreadyUpToDate) {
			return nil
		}
		return err
	}

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	_, err := gogit.PlainClone(dest, true /* bare/mirror */, &gogit.CloneOptions{
		URL:      cloneURL,
		Auth:     auth,
		Tags:     gogit.AllTags,
		Progress: os.Stderr,
	})
	return err
}

func main() {
	flag.Parse()

	if *flagURL == "" {
		slog.Error("--url is required")
		os.Exit(1)
	}

	slog.Info("fetching project list", "url", *flagURL)
	projects, err := fetchProjects(*flagURL, *flagToken)
	if err != nil {
		slog.Error("failed to fetch projects", "err", err)
		os.Exit(1)
	}
	slog.Info("total projects", "count", len(projects))

	if err := os.MkdirAll(*flagOutput, 0o755); err != nil {
		slog.Error("mkdir output", "err", err)
		os.Exit(1)
	}

	queue := make(chan project, len(projects))
	for _, p := range projects {
		queue <- p
	}
	close(queue)

	var (
		wg     sync.WaitGroup
		ok     atomic.Int64
		failed atomic.Int64
	)

	for i := 0; i < *flagWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range queue {
				slog.Info("cloning", "repo", p.PathWithNamespace)
				if err := cloneProject(p, *flagOutput, *flagToken, *flagSSH); err != nil {
					slog.Error("clone failed", "repo", p.PathWithNamespace, "err", err)
					failed.Add(1)
				} else {
					ok.Add(1)
				}
			}
		}()
	}

	wg.Wait()
	slog.Info("done", "ok", ok.Load(), "failed", failed.Load())
}
