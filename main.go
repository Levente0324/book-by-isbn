package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/joho/godotenv"
)

type Product struct {
	Site  string `json:"site"`
	Title string `json:"title"`
	Price string `json:"price"`
	URL   string `json:"url"`
}

type BookOffer struct {
	Site      string `json:"site"`
	Price     string `json:"price"`
	URL       string `json:"url"`
	Seller    string `json:"seller,omitempty"`
	Condition string `json:"condition,omitempty"`
}

type BookResponse struct {
	ISBN        string      `json:"isbn"`
	Title       string      `json:"title"`
	Offers      []BookOffer `json:"offers"`
	TotalOffers int         `json:"totalOffers"`
}

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
	},
}

func scrape(siteName, targetUrl string) ([]BookOffer, string, error) {
	apiKey := os.Getenv("SCRAPER_API_KEY")
	scrapeURL := fmt.Sprintf("http://api.scraperapi.com?api_key=%s&url=%s&autoparse=true", apiKey, targetUrl)

	log.Printf("Scraping %s with URL: %s", siteName, targetUrl)

	resp, err := httpClient.Get(scrapeURL)
	if err != nil {
		log.Printf("Error scraping %s: %v", siteName, err)
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Error status from %s: %d", siteName, resp.StatusCode)
		return nil, "", fmt.Errorf("received status code %d from scraper API", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading response body from %s: %v", siteName, err)
		return nil, "", err
	}

	log.Printf("Raw response from %s: %s", siteName, string(body))

	switch siteName {
	case "Amazon":
		var response struct {
			Results []map[string]interface{} `json:"results"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			log.Printf("Error decoding Amazon response: %v", err)
			return nil, "", err
		}
		return convertAmazonProducts(response.Results)
	case "eBay":
		var items []map[string]interface{}
		if err := json.Unmarshal(body, &items); err != nil {
			log.Printf("Error decoding eBay response: %v", err)
			return nil, "", err
		}
		return convertEbayProducts(items)
	default:
		return nil, "", fmt.Errorf("unknown site: %s", siteName)
	}
}

func convertAmazonProducts(items []map[string]interface{}) ([]BookOffer, string, error) {
	var results []BookOffer
	bookTitle := ""

	for _, item := range items {
		name, _ := item["name"].(string)
		if bookTitle == "" && name != "" {
			bookTitle = name
		}

		priceStr := ""
		if price, ok := item["price"].(float64); ok {
			priceStr = fmt.Sprintf("$%.2f", price)
		} else if priceString, ok := item["price_string"].(string); ok {
			priceStr = priceString
		}
		url, _ := item["url"].(string)

		if name != "" {
			offer := BookOffer{
				Site:  "Amazon",
				Price: priceStr,
				URL:   url,
			}

			// Try to get seller if available
			if seller, ok := item["seller"].(string); ok {
				offer.Seller = seller
			}

			results = append(results, offer)
		}
	}
	log.Printf("Found %d offers from Amazon", len(results))
	return results, bookTitle, nil
}

func convertEbayProducts(items []map[string]interface{}) ([]BookOffer, string, error) {
	var results []BookOffer
	bookTitle := ""

	for _, item := range items {
		// Get basic item info
		title, _ := item["title"].(string) // Changed from product_title
		url, _ := item["url"].(string)     // Changed from product_url

		if title == "" {
			// Try alternate fields
			title, _ = item["product_title"].(string)
			url, _ = item["product_url"].(string)
		}

		// Clean up title - remove Japanese text and other suffixes
		if title != "" {
			title = strings.Split(title, "新し")[0]
			title = strings.Split(title, " - New")[0]
			title = strings.Split(title, " - Used")[0]
			title = strings.TrimSpace(title)
			if bookTitle == "" {
				bookTitle = title
			}
		}

		// Try multiple price formats
		var price float64
		var isUSD bool

		if priceObj, ok := item["price"].(map[string]interface{}); ok {
			// Try direct price object
			if currency, ok := priceObj["currency"].(string); ok {
				isUSD = currency == "USD"
			}
			if value, ok := priceObj["value"].(float64); ok && isUSD {
				price = value
			}
		} else if priceObj, ok := item["item_price"].(map[string]interface{}); ok {
			// Try item_price object
			if currency, ok := priceObj["currency"].(string); ok {
				isUSD = currency == "USD"
			}
			if value, ok := priceObj["value"].(float64); ok && isUSD {
				price = value
			}
		} else if priceStr, ok := item["price"].(string); ok {
			// Try direct price string
			if strings.HasPrefix(priceStr, "$") {
				isUSD = true
				priceNum := strings.TrimPrefix(priceStr, "$")
				if p, err := strconv.ParseFloat(priceNum, 64); err == nil {
					price = p
				}
			}
		}

		// Only add items with USD prices
		if isUSD && price > 0 && price < 1000 && url != "" { // Added price sanity check
			offer := BookOffer{
				Site:  "eBay",
				Price: fmt.Sprintf("$%.2f", price),
				URL:   url,
			}

			// Add condition if available
			if condition, ok := item["condition"].(string); ok {
				offer.Condition = condition
			}

			results = append(results, offer)
		}
	}

	log.Printf("Found %d valid USD offers from eBay", len(results))
	return results, bookTitle, nil
}

// Add result channel type
type scrapeResult struct {
	offers []BookOffer
	title  string
	err    error
	site   string
}

func main() {
	_ = godotenv.Load()

	app := fiber.New()

	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("📚 College Book Price API is live!")
	})

	app.Get("/prices", func(c *fiber.Ctx) error {
		isbn := c.Query("isbn")
		if isbn == "" {
			return c.Status(400).JSON(fiber.Map{
				"error": "Missing 'isbn' query parameter",
			})
		}

		results := make(chan scrapeResult, 2)

		go func() {
			amazonURL := fmt.Sprintf("https://www.amazon.com/s?k=%s", isbn)
			offers, title, err := scrape("Amazon", amazonURL)
			results <- scrapeResult{offers: offers, title: title, err: err, site: "Amazon"}
		}()

		go func() {
			// Updated eBay URL parameters
			ebayURL := fmt.Sprintf("https://www.ebay.com/sch/i.html?_nkw=%s&_sacat=267&LH_PrefLoc=2&LH_BIN=1&_ipg=100", isbn)
			offers, title, err := scrape("eBay", ebayURL)
			results <- scrapeResult{offers: offers, title: title, err: err, site: "eBay"}
		}()

		// Collect results with timeout
		var amazonResult, ebayResult scrapeResult
		timeout := time.After(12 * time.Second)

		for i := 0; i < 2; i++ {
			select {
			case result := <-results:
				if result.site == "Amazon" {
					amazonResult = result
				} else {
					ebayResult = result
				}
			case <-timeout:
				return c.Status(500).JSON(fiber.Map{
					"error": "Request timeout",
				})
			}
		}

		// Handle errors
		if amazonResult.err != nil && ebayResult.err != nil {
			return c.Status(500).JSON(fiber.Map{
				"error": "All scraping attempts failed",
				"details": fiber.Map{
					"amazon_error": amazonResult.err.Error(),
					"ebay_error":   ebayResult.err.Error(),
				},
			})
		}

		// Initialize empty slices if needed
		if amazonResult.offers == nil {
			amazonResult.offers = []BookOffer{}
		}
		if ebayResult.offers == nil {
			ebayResult.offers = []BookOffer{}
		}

		allOffers := append(amazonResult.offers, ebayResult.offers...)

		// Choose the title - prefer Amazon's title
		bookTitle := amazonResult.title
		if bookTitle == "" {
			bookTitle = ebayResult.title
		}

		// If we got no results at all, return an appropriate message
		if len(allOffers) == 0 {
			return c.JSON(BookResponse{
				ISBN:        isbn,
				Title:       "",
				Offers:      []BookOffer{},
				TotalOffers: 0,
			})
		}

		return c.JSON(BookResponse{
			ISBN:        isbn,
			Title:       bookTitle,
			Offers:      allOffers,
			TotalOffers: len(allOffers),
		})
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	log.Printf("🚀 Running on port %s", port)
	log.Fatal(app.Listen(":" + port))
}
