package api

import (
	"archive/zip"
	"encoding/xml"
	"io"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"telecloud/tgclient"

	"github.com/gin-gonic/gin"
)

// --- XML structures for EPUB parsing ---

type epubContainer struct {
	RootFiles []struct {
		FullPath  string `xml:"full-path,attr"`
		MediaType string `xml:"media-type,attr"`
	} `xml:"rootfiles>rootfile"`
}

type epubOPF struct {
	Metadata struct {
		Title   []string `xml:"title"`
		Creator []string `xml:"creator"`
	} `xml:"metadata"`
	Manifest struct {
		Items []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			MediaType  string `xml:"media-type,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		Toc      string `xml:"toc,attr"`
		ItemRefs []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
}

type epubNCX struct {
	NavMap struct {
		NavPoints []epubNavPoint `xml:"navPoint"`
	} `xml:"navMap"`
}

type epubNavPoint struct {
	Label   string `xml:"navLabel>text"`
	Content struct {
		Src string `xml:"src,attr"`
	} `xml:"content"`
	Children []epubNavPoint `xml:"navPoint"`
}

// --- JSON response structures ---

type EpubSpineEntry struct {
	Href      string `json:"href"`
	MediaType string `json:"mediaType"`
}

type EpubTocEntry struct {
	Label    string         `json:"label"`
	Href     string         `json:"href"`
	Children []EpubTocEntry `json:"children,omitempty"`
}

// --- Handlers ---

func (h *Handler) handleGetEpubMeta(c *gin.Context) {
	h.serveEpubMeta(c, false)
}

func (h *Handler) handleGetSharedEpubMeta(c *gin.Context) {
	h.serveEpubMeta(c, true)
}

func (h *Handler) serveEpubMeta(c *gin.Context, isShare bool) {
	// Reuse resolveComicFile — it resolves any file by ID with auth, doesn't check extension
	item, err := h.resolveComicFile(c, isShare)
	if err != nil {
		switch err.Error() {
		case "unauthorized":
			c.JSON(http.StatusUnauthorized, gin.H{"error": "password_required"})
		case "forbidden":
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		default:
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		}
		return
	}

	if isShare && item.Size > 150*1024*1024 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file_too_large"})
		return
	}

	if ext := strings.ToLower(filepath.Ext(item.Filename)); ext != ".epub" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not_epub"})
		return
	}

	reader, err := tgclient.GetTelegramFileReader(c.Request.Context(), item, h.cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "stream_error"})
		return
	}
	defer reader.Close()

	rAt := &readerAt{rs: reader, fileID: item.ID, fileSize: item.Size}
	ensureZipMetadataCached(item.ID, item.Size, rAt)
	zr, err := zip.NewReader(rAt, item.Size)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "zip_error"})
		return
	}

	// 1. Read container.xml → find OPF path
	containerData, err := readZipEntry(zr, "META-INF/container.xml")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid_epub"})
		return
	}

	var container epubContainer
	if err := xml.Unmarshal(containerData, &container); err != nil || len(container.RootFiles) == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "container_parse_error"})
		return
	}

	opfPath := container.RootFiles[0].FullPath
	opfDir := path.Dir(opfPath)
	if opfDir == "." {
		opfDir = ""
	}

	// 2. Read OPF → manifest, spine, metadata
	opfData, err := readZipEntry(zr, opfPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "opf_not_found"})
		return
	}

	var opf epubOPF
	if err := xml.Unmarshal(opfData, &opf); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "opf_parse_error"})
		return
	}

	// Build manifest map (id → item)
	type manifestItem struct {
		Href      string
		MediaType string
	}
	manifest := make(map[string]manifestItem, len(opf.Manifest.Items))
	for _, it := range opf.Manifest.Items {
		manifest[it.ID] = manifestItem{Href: it.Href, MediaType: it.MediaType}
	}

	// 3. Build spine (reading order)
	spine := make([]EpubSpineEntry, 0, len(opf.Spine.ItemRefs))
	for _, ref := range opf.Spine.ItemRefs {
		if it, ok := manifest[ref.IDRef]; ok {
			href := it.Href
			if opfDir != "" {
				href = opfDir + "/" + href
			}
			spine = append(spine, EpubSpineEntry{Href: href, MediaType: it.MediaType})
		}
	}

	// 4. Parse TOC (try NCX first, then fallback to EPUB 3 navigation document)
	var toc []EpubTocEntry
	if opf.Spine.Toc != "" {
		if ncxItem, ok := manifest[opf.Spine.Toc]; ok {
			ncxPath := ncxItem.Href
			if opfDir != "" {
				ncxPath = opfDir + "/" + ncxPath
			}
			if data, err := readZipEntry(zr, ncxPath); err == nil {
				var ncx epubNCX
				if xml.Unmarshal(data, &ncx) == nil {
					toc = convertNavPoints(ncx.NavMap.NavPoints, opfDir)
				}
			}
		}
	}

	// Fallback to EPUB 3 nav document if NCX is missing or empty
	if len(toc) == 0 {
		for _, it := range opf.Manifest.Items {
			// standard EPUB 3 nav property is "nav", or ID is "nav", or check file name patterns
			if strings.Contains(it.Properties, "nav") || it.ID == "nav" || strings.Contains(strings.ToLower(it.Href), "nav.xhtml") || strings.Contains(strings.ToLower(it.Href), "nav.html") {
				navPath := it.Href
				if opfDir != "" {
					navPath = opfDir + "/" + navPath
				}
				if data, err := readZipEntry(zr, navPath); err == nil {
					toc = parseEPUB3Nav(data, opfDir)
					if len(toc) > 0 {
						break
					}
				}
			}
		}
	}

	// 5. Extract metadata
	title := ""
	author := ""
	if len(opf.Metadata.Title) > 0 {
		title = opf.Metadata.Title[0]
	}
	if len(opf.Metadata.Creator) > 0 {
		author = opf.Metadata.Creator[0]
	}

	c.JSON(http.StatusOK, gin.H{
		"title":  title,
		"author": author,
		"spine":  spine,
		"toc":    toc,
	})
}

func (h *Handler) handleGetEpubResource(c *gin.Context) {
	h.serveEpubResource(c, false)
}

func (h *Handler) handleGetSharedEpubResource(c *gin.Context) {
	h.serveEpubResource(c, true)
}

func (h *Handler) serveEpubResource(c *gin.Context, isShare bool) {
	item, err := h.resolveComicFile(c, isShare)
	if err != nil {
		switch err.Error() {
		case "unauthorized":
			c.AbortWithStatus(http.StatusUnauthorized)
		case "forbidden":
			c.AbortWithStatus(http.StatusForbidden)
		default:
			c.AbortWithStatus(http.StatusNotFound)
		}
		return
	}

	if isShare && item.Size > 150*1024*1024 {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	resourcePath := strings.TrimPrefix(c.Param("path"), "/")
	if resourcePath == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if ext := strings.ToLower(filepath.Ext(item.Filename)); ext != ".epub" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// 1. Check resource cache first to avoid Telegram reader overhead
	if data, cachedMime, found := getCachedZipResource(item.ID, resourcePath); found {
		c.Header("Content-Type", cachedMime)
		c.Header("Cache-Control", "private, max-age=86400")
		c.Writer.Write(data)
		return
	}

	reader, err := tgclient.GetTelegramFileReader(c.Request.Context(), item, h.cfg)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	defer reader.Close()

	rAt := &readerAt{rs: reader, fileID: item.ID, fileSize: item.Size}
	ensureZipMetadataCached(item.ID, item.Size, rAt)
	zr, err := zip.NewReader(rAt, item.Size)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// Find entry in ZIP (exact match, then case-insensitive fallback)
	var target *zip.File
	for _, f := range zr.File {
		if f.Name == resourcePath {
			target = f
			break
		}
	}
	if target == nil {
		lower := strings.ToLower(resourcePath)
		for _, f := range zr.File {
			if strings.ToLower(f.Name) == lower {
				target = f
				break
			}
		}
	}
	if target == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	fr, err := target.Open()
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	defer fr.Close()

	// Detect MIME type
	mimeType := mime.TypeByExtension(filepath.Ext(target.Name))
	if mimeType == "" {
		lower := strings.ToLower(target.Name)
		switch {
		case strings.HasSuffix(lower, ".xhtml"):
			mimeType = "application/xhtml+xml"
		case strings.HasSuffix(lower, ".css"):
			mimeType = "text/css"
		case strings.HasSuffix(lower, ".ncx"):
			mimeType = "application/x-dtbncx+xml"
		case strings.HasSuffix(lower, ".otf"), strings.HasSuffix(lower, ".ttf"):
			mimeType = "font/" + strings.TrimPrefix(filepath.Ext(lower), ".")
		default:
			mimeType = "application/octet-stream"
		}
	}

	c.Header("Content-Type", mimeType)
	c.Header("Cache-Control", "private, max-age=86400") // 24h browser cache

	// 2. Read and cache if resource size fits criteria (<2MB)
	if target.UncompressedSize64 <= 2*1024*1024 {
		data, err := io.ReadAll(fr)
		if err == nil {
			setCachedZipResource(item.ID, target.Name, data, mimeType)
			c.Writer.Write(data)
			return
		}
	}

	io.Copy(c.Writer, fr)
}

// --- Helpers ---

func readZipEntry(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	// Case-insensitive fallback
	lower := strings.ToLower(name)
	for _, f := range zr.File {
		if strings.ToLower(f.Name) == lower {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, io.EOF
}

func convertNavPoints(points []epubNavPoint, opfDir string) []EpubTocEntry {
	entries := make([]EpubTocEntry, 0, len(points))
	for _, p := range points {
		href := p.Content.Src
		if opfDir != "" && href != "" && !strings.HasPrefix(href, "/") {
			href = opfDir + "/" + href
		}
		entry := EpubTocEntry{Label: p.Label, Href: href}
		if len(p.Children) > 0 {
			entry.Children = convertNavPoints(p.Children, opfDir)
		}
		entries = append(entries, entry)
	}
	return entries
}

func parseEPUB3Nav(data []byte, opfDir string) []EpubTocEntry {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity

	// listStack tracks the levels of lists (TOC entries at each level)
	// We start with one empty list at the root level (index 0)
	var listStack [][]EpubTocEntry
	listStack = append(listStack, []EpubTocEntry{})

	// activeEntry wraps a TOC entry with a text builder to accumulate its label
	type activeEntry struct {
		entry *EpubTocEntry
		text  strings.Builder
	}

	// entryStack tracks the path of active <li> elements
	var entryStack []*activeEntry

	inNav := false
	parsedTOC := false

	for {
		t, err := decoder.Token()
		if err != nil {
			break
		}
		switch se := t.(type) {
		case xml.StartElement:
			name := strings.ToLower(se.Name.Local)
			if name == "nav" {
				isTOC := false
				for _, attr := range se.Attr {
					if attr.Name.Local == "type" && attr.Value == "toc" {
						isTOC = true
						break
					}
				}
				if isTOC || !parsedTOC {
					inNav = true
					if isTOC && len(listStack[0]) > 0 {
						// Reset if we found an explicit TOC nav after a generic/fallback one
						listStack[0] = []EpubTocEntry{}
					}
				}
			} else if inNav {
				switch name {
				case "ol":
					// Start a new list level
					listStack = append(listStack, []EpubTocEntry{})
				case "li":
					// Start a new entry
					entryStack = append(entryStack, &activeEntry{
						entry: &EpubTocEntry{},
					})
				case "a":
					if len(entryStack) > 0 {
						for _, attr := range se.Attr {
							if attr.Name.Local == "href" {
								href := attr.Value
								if opfDir != "" && href != "" && !strings.HasPrefix(href, "/") {
									href = opfDir + "/" + href
								}
								entryStack[len(entryStack)-1].entry.Href = href
							}
						}
					}
				}
			}
		case xml.CharData:
			if inNav && len(entryStack) > 0 && len(listStack) == len(entryStack)+1 {
				entryStack[len(entryStack)-1].text.Write(se)
			}
		case xml.EndElement:
			name := strings.ToLower(se.Name.Local)
			if name == "nav" {
				if inNav {
					inNav = false
					if len(listStack[0]) > 0 {
						parsedTOC = true
					}
				}
			} else if inNav {
				switch name {
				case "li":
					if len(entryStack) > 0 {
						// Pop active entry
						active := entryStack[len(entryStack)-1]
						entryStack = entryStack[:len(entryStack)-1]

						// Set label
						active.entry.Label = strings.TrimSpace(active.text.String())

						// Append to the current active list level
						L := len(listStack) - 1
						listStack[L] = append(listStack[L], *active.entry)
					}
				case "ol":
					L := len(listStack) - 1
					if L > 0 {
						poppedList := listStack[L]
						listStack = listStack[:L]

						// If there is an active parent entry, these are its children
						if len(entryStack) > 0 {
							entryStack[len(entryStack)-1].entry.Children = poppedList
						} else {
							// Otherwise, they are appended/merged into the parent list level
							parentL := L - 1
							if len(listStack[parentL]) > 0 {
								// Set as children of the last element in the parent list level
								listStack[parentL][len(listStack[parentL])-1].Children = poppedList
							}
						}
					}
				}
			}
		}
	}

	return listStack[0]
}
