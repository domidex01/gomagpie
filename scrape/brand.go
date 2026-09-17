package scrape

import (
	"context"

	"gomagpie/clean"
	"gomagpie/fetch"
)

// BrandResult is one brand extraction: page identity plus the zero-LLM
// brand surface.
type BrandResult struct {
	URL      string `json:"url"`
	FinalURL string `json:"final_url"`
	Title    string `json:"title"`
	clean.BrandInfo
}

// BrandPage fetches rawURL through the Fetcher seam (Run cannot serve
// brand — Result carries no raw HTML) and returns the pure clean.Brand
// surface plus favicon/og:image via clean.HarvestMetadata, never a second
// copy of that logic. Blocked pages fail as typed quality errors, never
// empty brand records. Zero LLM.
func BrandPage(ctx context.Context, d Deps, rawURL string) (BrandResult, error) {
	var vf = d.Fetcher
	if vf == nil {
		static, err := fetch.NewStaticFetcher()
		if err != nil {
			return BrandResult{}, err
		}
		vf = static
	}
	resp, err := vf.Fetch(ctx, fetch.FetchRequest{URL: rawURL})
	if err != nil {
		return BrandResult{}, err
	}
	cleaned, err := clean.Clean(ctx, clean.RawPage{
		HTML: resp.HTML, URL: resp.URL, FinalURL: resp.FinalURL, StatusCode: resp.StatusCode,
	})
	if err != nil {
		return BrandResult{}, err
	}
	if cleaned.Quality != clean.IssueNone {
		return BrandResult{}, &clean.QualityError{Issue: cleaned.Quality, URL: rawURL}
	}
	info := clean.Brand(resp.HTML)
	meta := clean.HarvestMetadata(resp.HTML, cleaned.FinalURL, "")
	info.Favicon = meta.Favicon
	if info.Logo == "" {
		info.Logo = meta.Image // og:image is the honest logo fallback
	}
	return BrandResult{
		URL: resp.URL, FinalURL: cleaned.FinalURL, Title: cleaned.Title, BrandInfo: info,
	}, nil
}
