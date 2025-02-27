package classes

import (
	"context"
	"database/sql"
	"path"
	"strings"

	"github.com/Vinubaba/SANTC-API/api/ageranges"
	. "github.com/Vinubaba/SANTC-API/common/api"
	"github.com/Vinubaba/SANTC-API/common/firebase/claims"
	"github.com/Vinubaba/SANTC-API/common/log"
	"github.com/Vinubaba/SANTC-API/common/storage"
	"github.com/Vinubaba/SANTC-API/common/store"

	"github.com/jinzhu/gorm"
	"github.com/pkg/errors"
)

var (
	ErrEmptyClass             = errors.New("classId cannot be empty")
	ErrEmptyAgeRange          = errors.New("please specify an age range")
	ErrCreateDifferentDaycare = errors.New("you can't add a class to a different daycare of you")
)

type Service interface {
	AddClass(ctx context.Context, request ClassTransport) (store.Class, error)
	GetClass(ctx context.Context, request ClassTransport) (store.Class, error)
	DeleteClass(ctx context.Context, request ClassTransport) error
	ListClasses(ctx context.Context) ([]store.Class, error)
	UpdateClass(ctx context.Context, request ClassTransport) (store.Class, error)
}

type ClassService struct {
	Store interface {
		Tx() *gorm.DB

		AddClass(tx *gorm.DB, class store.Class) (store.Class, error)
		UpdateClass(tx *gorm.DB, class store.Class) (store.Class, error)
		GetClass(tx *gorm.DB, classId string, options store.SearchOptions) (store.Class, error)
		ListClasses(tx *gorm.DB, options store.SearchOptions) ([]store.Class, error)
		DeleteClass(tx *gorm.DB, classId string) error

		GetAgeRange(tx *gorm.DB, ageRangeId string, options store.SearchOptions) (store.AgeRange, error)
	} `inject:""`
	Storage storage.Storage `inject:""`
	Logger  *log.Logger     `inject:""`
}

func (c *ClassService) AddClass(ctx context.Context, request ClassTransport) (store.Class, error) {
	if claims.IsAdmin(ctx) && IsNilOrEmpty(request.DaycareId) {
		return store.Class{}, errors.New("as an admin, you must specify the a daycareId")
	} else {
		daycareId := claims.GetDaycareId(ctx)
		// default to requester daycare (e.g office manager)
		if IsNilOrEmpty(request.DaycareId) {
			request.DaycareId = &daycareId
		}

		if daycareId != *request.DaycareId {
			return store.Class{}, ErrCreateDifferentDaycare
		}
	}

	var err error

	if (ageranges.AgeRangeTransport{}) == request.AgeRange {
		return store.Class{}, ErrEmptyAgeRange
	}
	// Ensure same daycare for age range and class
	request.AgeRange.DaycareId = request.DaycareId

	// Ensure specified age range is part of the same daycare
	if !IsNilOrEmpty(request.AgeRange.Id) {
		searchOptions := claims.GetDefaultSearchOptions(ctx)
		searchOptions.DaycareId = *request.DaycareId
		_, err = c.Store.GetAgeRange(nil, *request.AgeRange.Id, searchOptions)
		if err != nil {
			return store.Class{}, errors.Wrap(err, "failed to add class")
		}
	}

	imageUri, err := c.Storage.Store(ctx, *request.ImageUri, c.storageFolder(*request.DaycareId))
	if err != nil {
		return store.Class{}, errors.Wrap(err, "failed to store image")
	}
	request.ImageUri = &imageUri

	class, err := c.Store.AddClass(nil, transportToStore(request))
	if err != nil {
		return store.Class{}, errors.Wrap(err, "failed to add class")
	}

	uri, err := c.Storage.Get(ctx, *request.ImageUri)
	if err != nil {
		return store.Class{}, errors.Wrap(err, "failed to generate image uri")
	}
	class.ImageUri = store.DbNullString(&uri)

	return class, nil
}

func (c *ClassService) storageFolder(daycareId string) string {
	return path.Join("daycares", daycareId, "classes")
}

func (c *ClassService) GetClass(ctx context.Context, request ClassTransport) (store.Class, error) {
	searchOptions := claims.GetDefaultSearchOptions(ctx)
	class, err := c.Store.GetClass(nil, *request.Id, searchOptions)
	if err != nil {
		return class, errors.Wrap(err, "failed to get class")
	}
	c.setBucketUri(ctx, &class)
	return class, nil
}

func (c *ClassService) DeleteClass(ctx context.Context, request ClassTransport) error {
	searchOptions := claims.GetDefaultSearchOptions(ctx)
	class, err := c.Store.GetClass(nil, *request.Id, searchOptions)
	if err != nil {
		return errors.Wrap(err, "failed to delete class")
	}

	if err := c.Store.DeleteClass(nil, *request.Id); err != nil {
		return errors.Wrap(err, "failed to delete class")
	}

	if err := c.Storage.Delete(ctx, class.ImageUri.String); err != nil {
		return errors.Wrap(err, "failed to delete class image")
	}

	return nil
}

func (c *ClassService) setBucketUri(ctx context.Context, class *store.Class) {
	if class.ImageUri.String == "" {
		return
	}
	uri, err := c.Storage.Get(ctx, class.ImageUri.String)
	if err != nil {
		c.Logger.Warn(ctx, "failed to generate image uri", "imageUri", class.ImageUri, "err", err.Error())
	}
	class.ImageUri = store.DbNullString(&uri)
}

func (c *ClassService) ListClasses(ctx context.Context) ([]store.Class, error) {
	searchOptions := claims.GetDefaultSearchOptions(ctx)
	classes, err := c.Store.ListClasses(nil, searchOptions)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list classes")
	}

	for i := 0; i < len(classes); i++ {
		uri, err := c.Storage.Get(ctx, classes[i].ImageUri.String)
		if err != nil {
			return []store.Class{}, errors.Wrap(err, "failed to generate image uri")
		}
		classes[i].ImageUri = store.DbNullString(&uri)
	}

	return classes, nil
}

func (c *ClassService) UpdateClass(ctx context.Context, request ClassTransport) (store.Class, error) {
	var err error

	if IsNilOrEmpty(request.Id) {
		return store.Class{}, ErrEmptyClass
	}

	searchOptions := claims.GetDefaultSearchOptions(ctx)
	class, err := c.Store.GetClass(nil, *request.Id, searchOptions)
	if err != nil {
		return store.Class{}, errors.Wrap(err, "failed to update class")
	}

	if !IsNilOrEmpty(request.AgeRange.Id) {
		_, err = c.Store.GetAgeRange(nil, *request.AgeRange.Id, searchOptions)
		if err != nil {
			return store.Class{}, errors.Wrap(err, "failed to update class")
		}
	}

	if !IsNilOrEmpty(request.ImageUri) {
		imageUri, err := c.Storage.Store(ctx, *request.ImageUri, c.storageFolder(class.DaycareId.String))
		if err != nil {
			return store.Class{}, errors.Wrap(err, "failed to store image")
		}
		request.ImageUri = &imageUri
	}

	class, err = c.Store.UpdateClass(nil, transportToStore(request))
	if err != nil {
		return class, errors.Wrap(err, "failed to update class")
	}
	c.setBucketUri(ctx, &class)
	return class, nil
}

func (c *ClassService) getBucketUri(ctx context.Context, imgPath string) sql.NullString {
	if imgPath == "" || strings.Contains(imgPath, "/") {
		return sql.NullString{
			String: "",
			Valid:  false,
		}
	}
	uri, err := c.Storage.Get(ctx, imgPath)
	if err != nil {
		c.Logger.Warn(ctx, "failed to generate image uri", "imageUri", imgPath, "err", err.Error())
	}
	return store.DbNullString(&uri)
}

// ServiceMiddleware is a chainable behavior modifier for classService.
type ServiceMiddleware func(ClassService) ClassService
