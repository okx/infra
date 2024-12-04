package proxyd

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

func NewRedisClient(url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(opts)
	return client, nil
}

func NewRedisClusterClient(url string) (*redis.ClusterClient, error) {
	opts, err := redis.ParseClusterURL(url)
	if err != nil {
		return nil, err
	}
	client := redis.NewClusterClient(opts)
	return client, nil
}

func CheckRedisConnection(client *redis.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return wrapErr(err, "error connecting to redis")
	}

	return nil
}

func CheckRedisClusterConnection(client *redis.ClusterClient) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return wrapErr(err, "error connecting to redis")
	}

	return nil
}
