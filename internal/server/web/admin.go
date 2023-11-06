package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/bricks-cloud/bricksllm/internal/apikey"
	"github.com/bricks-cloud/bricksllm/internal/event"
	"github.com/bricks-cloud/bricksllm/internal/key"
	"github.com/bricks-cloud/bricksllm/internal/util"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type KeyManager interface {
	GetKeysByTag(tag string) ([]*key.ResponseKey, error)
	UpdateKey(id string, key *key.UpdateKey) (*key.ResponseKey, error)
	CreateKey(key *key.RequestKey) (*key.ResponseKey, error)
	DeleteKey(id string) error
}

type ApiKeyManager interface {
	SetKey(key *apikey.RequestApiKey) error
	GetKey(provider string, keyName string) (string, error)
}

type KeyReportingManager interface {
	GetKeyReporting(keyId string) (*key.KeyReporting, error)
	GetEventReporting(e *event.ReportingRequest) (*event.ReportingResponse, error)
}

type ErrorResponse struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail"`
	Instance string `json:"instance"`
}

type AdminServer struct {
	server *http.Server
	log    *zap.Logger
	m      KeyManager
}

func NewAdminServer(log *zap.Logger, mode string, m KeyManager, krm KeyReportingManager, apikm ApiKeyManager) (*AdminServer, error) {
	router := gin.New()

	prod := mode == "production"
	router.Use(getAdminLogger(log, "admin", prod))

	router.GET("/api/key-management/keys", getGetKeysHandler(m, log, prod))
	router.PUT("/api/key-management/keys", getCreateKeyHandler(m, log, prod))
	router.PATCH("/api/key-management/keys/:id", getUpdateKeyHandler(m, log, prod))
	router.DELETE("/api/key-management/keys/:id", getDeleteKeyHandler(m, log, prod))

	router.GET("/api/reporting/keys/:id", getGetKeyReportingHandler(krm, log, prod))
	router.GET("/api/reporting/events", getGetEventMetrics(krm, log, prod))

	router.PUT("/api/api-key-management/keys", getSetApiKeysHandler(apikm, log, prod))

	srv := &http.Server{
		Addr:    ":8001",
		Handler: router,
	}

	return &AdminServer{
		log:    log,
		server: srv,
		m:      m,
	}, nil
}

func (as *AdminServer) Run() {
	go func() {
		as.log.Info("admin server listening at 8001")
		as.log.Info("PORT 8001 | GET   | /api/key-management/keys is set up for retrieving keys using a query param called tag")
		as.log.Info("PORT 8001 | PUT   | /api/key-management/keys is set up for creating a key")
		as.log.Info("PORT 8001 | PATCH | /api/key-management/keys/:id is set up for updating a key using an id")

		if err := as.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			as.log.Sugar().Fatalf("error admin server listening: %v", err)
		}
	}()
}

func (as *AdminServer) Shutdown(ctx context.Context) error {
	if err := as.server.Shutdown(ctx); err != nil {
		as.log.Sugar().Infof("error shutting down admin server: %v", err)
		return err
	}

	return nil
}

func getSetApiKeysHandler(m ApiKeyManager, log *zap.Logger, prod bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := "/api/api-key-management/keys"
		data, err := io.ReadAll(c.Request.Body)
		id := c.Param(correlationId)
		if err != nil {
			logError(log, "error when reading key creation request body", prod, id, err)
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/request-body-read",
				Title:    "request body reader error",
				Status:   http.StatusInternalServerError,
				Detail:   err.Error(),
				Instance: path,
			})
			return
		}

		rak := &apikey.RequestApiKey{}
		err = json.Unmarshal(data, rak)
		if err != nil {
			logError(log, "error when unmarshalling api keys request body", prod, id, err)
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/json-unmarshal",
				Title:    "json unmarshaller error",
				Status:   http.StatusInternalServerError,
				Detail:   err.Error(),
				Instance: path,
			})
			return
		}

		err = m.SetKey(rak)
		if err != nil {
			logError(log, "error when setting api keys", prod, id, err)
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/setting-api-key",
				Title:    "setting api key error",
				Status:   http.StatusInternalServerError,
				Detail:   err.Error(),
				Instance: path,
			})
			return
		}

		c.Status(http.StatusOK)
	}
}

func getGetKeysHandler(m KeyManager, log *zap.Logger, prod bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		tag := c.Query("tag")
		path := "/api/key-management/keys"
		if len(tag) == 0 {
			c.JSON(http.StatusBadRequest, &ErrorResponse{
				Type:     "/errors/missing-query-tag",
				Title:    "tag is empty",
				Status:   http.StatusBadRequest,
				Detail:   "query param tag is missing from the request url. it is required for retrieving keys.",
				Instance: path,
			})
			return
		}

		id := c.Param(correlationId)
		keys, err := m.GetKeysByTag(tag)
		if err != nil {
			logError(log, "error when getting api keys by tag", prod, id, err)
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/getting-keys",
				Title:    "getting keys errored out",
				Status:   http.StatusInternalServerError,
				Detail:   err.Error(),
				Instance: path,
			})
			return
		}

		c.JSON(http.StatusOK, keys)
	}
}

type ValidationError interface {
	Error() string
	Validation()
}

func getCreateKeyHandler(m KeyManager, log *zap.Logger, prod bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := "/api/key-management/keys"
		if c == nil || c.Request == nil {
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/empty-context",
				Title:    "context is empty error",
				Status:   http.StatusInternalServerError,
				Detail:   "gin context is empty",
				Instance: path,
			})
			return
		}

		id := c.GetString(correlationId)
		data, err := io.ReadAll(c.Request.Body)
		if err != nil {
			logError(log, "error when reading key creation request body", prod, id, err)
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/request-body-read",
				Title:    "request body reader error",
				Status:   http.StatusInternalServerError,
				Detail:   err.Error(),
				Instance: path,
			})
			return
		}

		rk := &key.RequestKey{}
		err = json.Unmarshal(data, rk)
		if err != nil {
			logError(log, "error when unmarshalling key creation request body", prod, id, err)
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/json-unmarshal",
				Title:    "json unmarshaller error",
				Status:   http.StatusInternalServerError,
				Detail:   err.Error(),
				Instance: path,
			})
			return
		}

		resk, err := m.CreateKey(rk)
		if err != nil {
			if _, ok := err.(ValidationError); ok {
				c.JSON(http.StatusBadRequest, &ErrorResponse{
					Type:     "/errors/validation",
					Title:    "key validation failed",
					Status:   http.StatusBadRequest,
					Detail:   err.Error(),
					Instance: path,
				})
				return
			}

			logError(log, "error when creating api key", prod, id, err)
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/key-manager",
				Title:    "key creation error",
				Status:   http.StatusInternalServerError,
				Detail:   err.Error(),
				Instance: path,
			})
			return
		}

		c.JSON(http.StatusOK, resk)
	}
}

func getUpdateKeyHandler(m KeyManager, log *zap.Logger, prod bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := "/api/key-management/keys/:id"
		if c == nil || c.Request == nil {
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/empty-context",
				Title:    "context is empty error",
				Status:   http.StatusInternalServerError,
				Detail:   "gin context is empty",
				Instance: path,
			})
			return
		}

		id := c.Param("id")
		cid := c.Param(correlationId)
		if len(id) == 0 {
			c.JSON(http.StatusBadRequest, &ErrorResponse{
				Type:     "/errors/missing-param-id",
				Title:    "id is empty",
				Status:   http.StatusBadRequest,
				Detail:   "id url param is missing from the request url. it is required for updating a key.",
				Instance: path,
			})

			return
		}

		data, err := io.ReadAll(c.Request.Body)
		if err != nil {
			logError(log, "error when reading api key update request body", prod, cid, err)
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/request-body-read",
				Title:    "request body reader error",
				Status:   http.StatusInternalServerError,
				Detail:   err.Error(),
				Instance: path,
			})
			return
		}

		uk := &key.UpdateKey{}
		err = json.Unmarshal(data, uk)
		if err != nil {
			logError(log, "error when unmarshalling api key update request body", prod, cid, err)
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/json-unmarshal",
				Title:    "json unmarshaller error",
				Status:   http.StatusInternalServerError,
				Detail:   err.Error(),
				Instance: path,
			})
			return
		}

		resk, err := m.UpdateKey(id, uk)
		if err != nil {
			if _, ok := err.(ValidationError); ok {
				c.JSON(http.StatusBadRequest, &ErrorResponse{
					Type:     "/errors/validation",
					Title:    "key validation failed",
					Status:   http.StatusBadRequest,
					Detail:   err.Error(),
					Instance: path,
				})
				return
			}

			logError(log, "error when updating api key", prod, cid, err)
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/key-manager",
				Title:    "update key error",
				Status:   http.StatusInternalServerError,
				Detail:   err.Error(),
				Instance: path,
			})
			return
		}

		c.JSON(http.StatusOK, resk)
	}
}

func getAdminLogger(log *zap.Logger, prefix string, prod bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(correlationId, util.NewUuid())
		start := time.Now()
		c.Next()
		latency := time.Now().Sub(start).Milliseconds()
		if !prod {
			log.Sugar().Infof("%s | %d | %s | %s | %dms", prefix, c.Writer.Status(), c.Request.Method, c.FullPath(), latency)
		}

		if prod {
			log.Info("request to admin management api",
				zap.String(correlationId, c.GetString(correlationId)),
				zap.Int("code", c.Writer.Status()),
				zap.String("method", c.Request.Method),
				zap.String("path", c.FullPath()),
				zap.Int64("lantecyInMs", latency),
			)
		}
	}
}

func getDeleteKeyHandler(m KeyManager, log *zap.Logger, prod bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := "/api/key-management/keys/:id"
		if c == nil || c.Request == nil {
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/empty-context",
				Title:    "context is empty error",
				Status:   http.StatusInternalServerError,
				Detail:   "gin context is empty",
				Instance: path,
			})
			return
		}

		id := c.Param("id")
		cid := c.Param(correlationId)
		if len(id) == 0 {
			c.JSON(http.StatusBadRequest, &ErrorResponse{
				Type:     "/errors/missing-param-id",
				Title:    "id is empty",
				Status:   http.StatusBadRequest,
				Detail:   "id url param is missing from the request url. it is required for deleting a key.",
				Instance: path,
			})

			return
		}

		err := m.DeleteKey(id)
		if err != nil {
			logError(log, "error when deleting api key", prod, cid, err)
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/key-manager",
				Title:    "key deletion error",
				Status:   http.StatusInternalServerError,
				Detail:   err.Error(),
				Instance: path,
			})
			return
		}

		c.Status(http.StatusOK)
	}
}

type notFoundError interface {
	Error() string
	NotFound()
}

func validateEventReportingRequest(r *event.ReportingRequest) bool {
	if r.Start == 0 || r.End == 0 || r.Increment <= 0 {
		return false
	}

	if r.Start >= r.End {
		return false
	}

	return true
}

func getGetEventMetrics(m KeyReportingManager, log *zap.Logger, prod bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := "/api/reporting/events"
		if c == nil || c.Request == nil {
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/empty-context",
				Title:    "context is empty error",
				Status:   http.StatusInternalServerError,
				Detail:   "gin context is empty",
				Instance: path,
			})
			return
		}

		cid := c.Param(correlationId)
		data, err := io.ReadAll(c.Request.Body)
		if err != nil {
			logError(log, "error when reading event reporting request body", prod, cid, err)
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/request-body-read",
				Title:    "request body reader error",
				Status:   http.StatusInternalServerError,
				Detail:   err.Error(),
				Instance: path,
			})
			return
		}

		request := &event.ReportingRequest{}
		err = json.Unmarshal(data, request)
		if err != nil {
			logError(log, "error when unmarshalling event reporting request body", prod, cid, err)
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/json-unmarshal",
				Title:    "json unmarshaller error",
				Status:   http.StatusInternalServerError,
				Detail:   err.Error(),
				Instance: path,
			})
			return
		}

		if !validateEventReportingRequest(request) {
			err = fmt.Errorf("event reporting request %+v is not valid", request)
			logError(log, "invalid reporting request", prod, cid, err)
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/invalid-reporting-request",
				Title:    "invalid reporting request",
				Status:   http.StatusBadRequest,
				Detail:   err.Error(),
				Instance: path,
			})
			return
		}

		reportingResponse, err := m.GetEventReporting(request)
		if err != nil {
			logError(log, "error when getting event reporting", prod, cid, err)
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/event-reporting-manager",
				Title:    "event reporting error",
				Status:   http.StatusInternalServerError,
				Detail:   err.Error(),
				Instance: path,
			})
			return
		}

		c.JSON(http.StatusOK, reportingResponse)
	}
}

func getGetKeyReportingHandler(m KeyReportingManager, log *zap.Logger, prod bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := "/api/reporting/keys/:id"
		if c == nil || c.Request == nil {
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/empty-context",
				Title:    "context is empty error",
				Status:   http.StatusInternalServerError,
				Detail:   "gin context is empty",
				Instance: path,
			})
			return
		}

		id := c.Param("id")
		cid := c.Param(correlationId)
		if len(id) == 0 {
			c.JSON(http.StatusBadRequest, &ErrorResponse{
				Type:     "/errors/missing-param-id",
				Title:    "id is empty",
				Status:   http.StatusBadRequest,
				Detail:   "id url param is missing from the request url. it is required for retrieving api key reporting",
				Instance: path,
			})

			return
		}

		kr, err := m.GetKeyReporting(id)
		if err != nil {
			if _, ok := err.(notFoundError); ok {
				logError(log, "key not found", prod, cid, err)
				c.JSON(http.StatusInternalServerError, &ErrorResponse{
					Type:     "/errors/key-not-found",
					Title:    "key not found error",
					Status:   http.StatusNotFound,
					Detail:   err.Error(),
					Instance: path,
				})
				return
			}

			logError(log, "error when getting api key reporting", prod, cid, err)
			c.JSON(http.StatusInternalServerError, &ErrorResponse{
				Type:     "/errors/key-reporting-manager",
				Title:    "key reporting error",
				Status:   http.StatusInternalServerError,
				Detail:   err.Error(),
				Instance: path,
			})
			return
		}

		c.JSON(http.StatusOK, kr)
	}
}
