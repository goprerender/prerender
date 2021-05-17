Prerender
===========================

Prerender is a server writen in Golang that uses Headless Chrome or `headless-shell` docker image to render HTML and JS files out of any web page. The Prerender server listens for an http request, takes the URL and loads it in Headless Chrome, waits for the page to finish loading by waiting for the network to be idle, and then returns your content.

### In memory cache

Caches pages in memory with prerender `storage`.

#### URL

The URL you want to load. Returns HTML by default.

```
http://localhost:3000/render?url=https://www.example.com/
```

#### Sitemaps

Server use `sitemap.json` to load sitemap urls array.
