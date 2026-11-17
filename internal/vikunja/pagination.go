package vikunja

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// Lists in API v2 carry pagination in the response body. PerPage is the
// effective server limit, which may be lower than the requested page size.
type pageResult[T any] struct {
	Items      []T   `json:"items"`
	Page       int   `json:"page"`
	PerPage    int   `json:"per_page"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

func readPage[T any](ctx context.Context, c *Client, path string, number int, query url.Values) (pageResult[T], error) {
	if query == nil {
		query = url.Values{}
	}
	query.Set("page", strconv.Itoa(number))
	query.Set("per_page", "100")
	var result pageResult[T]
	if _, err := c.get(ctx, path+"?"+query.Encode(), &result); err != nil {
		return result, err
	}
	if result.Page != number || result.PerPage <= 0 || result.Total < 0 || result.TotalPages < 0 || len(result.Items) > result.PerPage || int64(len(result.Items)) > result.Total {
		return result, fmt.Errorf("invalid Vikunja pagination response")
	}
	if result.Total == 0 {
		if result.TotalPages > 1 || number != 1 {
			return result, fmt.Errorf("invalid Vikunja empty pagination response")
		}
	} else if int64(result.TotalPages) != 1+(result.Total-1)/int64(result.PerPage) || number > result.TotalPages || len(result.Items) == 0 {
		return result, fmt.Errorf("invalid Vikunja pagination response")
	}
	return result, nil
}
