package common

import (
	"fmt"
	"redigo/redis"
	"reflect"
	"strconv"
)

var pool *redis.Pool

func DefaultRedisPool() *redis.Pool {
	return pool
}

func MakeRedisPool(config RedisConfig) {
	if pool != nil {
		pool.Close()
	}
	pool = redis.NewPool(func() (redis.Conn, error) {
		c, err := redis.Dial("tcp", config.Addr)
		if err != nil {
			return nil, err
		}
		if config.Password != "" {
			_, err = c.Do("AUTH", config.Password)
			if err != nil {
				return nil, err
			}
		}
		_, err = c.Do("SELECT", config.Database)
		if err != nil {
			return nil, err
		}
		return c, nil
	}, config.MaxIdle)
}

type RedisInterface interface {
	GetKey() string
}

// Redis Object Wrapper
type RedisObject struct {
	Key string
	Obj interface{}
}

func (r *RedisObject) GetKey() string {
	return r.Key
}

type RedisStorer interface {
	RedisSave(redis.Conn) error
}

type RedisLoader interface {
	RedisLoad(redis.Conn) error
}

type RedisRemover interface {
	RedisRemove(redis.Conn) error
}

func RedisSave(v interface{}) error {
	r := pool.Get()
	defer r.Close()
	return RedisSaveWithConn(r, v)
}

func RedisSaveWithConn(r redis.Conn, v interface{}) (err error) {
	switch s := v.(type) {
	case RedisStorer:
		err = s.RedisSave(r)
	case RedisInterface:
		data, err := GobEncode(s)
		if err != nil {
			return err
		}
		_, err = r.Do("SET", s.GetKey(), data)
	default:
		err = fmt.Errorf("Unsupperted Type!")
	}
	return
}

func RedisLoad(v interface{}) error {
	r := pool.Get()
	defer r.Close()
	return RedisLoadWithConn(r, v)
}

func RedisLoadWithConn(r redis.Conn, v interface{}) (err error) {
	switch l := v.(type) {
	case RedisLoader:
		err = l.RedisLoad(r)
	case RedisInterface:
		data, err := redis.Bytes(r.Do("GET", l.GetKey()))
		if err != nil {
			return err
		}
		err = GobDecode(data, l)
	default:
		err = fmt.Errorf("Unsupperted Type!")
	}
	return
}

func RedisRemove(v interface{}) error {
	r := pool.Get()
	defer r.Close()
	return RedisRemoveWithConn(r, v)
}

func RedisRemoveWithConn(r redis.Conn, v interface{}) (err error) {
	switch rm := v.(type) {
	case RedisRemover:
		err = rm.RedisRemove(r)
	case RedisInterface:
		_, err = r.Do("DEL", rm.GetKey())
	default:
		err = fmt.Errorf("Unsupported Type!")
	}
	return
}

type RedisSlice struct {
	key   string
	slice reflect.Value
	sType reflect.Type
	eType reflect.Type
}

func MakeRedisSlice(key string, slicePtr interface{}) (*RedisSlice, error) {
	val := reflect.ValueOf(slicePtr)
	if val.Kind() != reflect.Ptr || val.IsNil() || val.Elem().Kind() != reflect.Slice {
		return nil, fmt.Errorf("MakeRedisSlice: Must be slice pointer type")
	}
	return &RedisSlice{
		key:   key,
		slice: reflect.Indirect(val),
		sType: val.Elem().Type(),
		eType: val.Elem().Type().Elem(),
	}, nil
}

func RedisSliceSave(key string, slicePtr interface{}) error {
	rs, err := MakeRedisSlice(key, slicePtr)
	if err != nil {
		return err
	}
	err = RedisSave(rs)
	return err
}

func RedisSliceLoad(key string, slicePtr interface{}) error {
	rs, err := MakeRedisSlice(key, slicePtr)
	if err != nil {
		return err
	}
	err = RedisLoad(rs)
	return err
}

func RedisSliceRemove(key string, slicePtr interface{}) error {
	rs, err := MakeRedisSlice(key, slicePtr)
	if err != nil {
		return err
	}
	err = RedisRemove(rs)
	return err
}

func (v *RedisSlice) RedisSave(r redis.Conn) error {
	if v.slice.Len() == 0 {
		return nil
	}

	var keys = make([]interface{}, v.slice.Len()+1)
	keys[0] = v.key
	for i := 0; i < v.slice.Len(); i++ {
		switch ro := v.slice.Index(i).Interface().(type) {
		case RedisInterface:
			keys[i+1] = ro.GetKey()
			RedisSaveWithConn(r, ro)
		default:
			keys[i+1] = ro
		}
	}
	_, err := r.Do("SADD", keys...)
	return err
}

func (v *RedisSlice) RedisLoad(r redis.Conn) error {
	elemType := reflect.Indirect(reflect.New(v.eType))
	switch elemType.Interface().(type) {
	case RedisInterface:
		reply, err := redis.Values(r.Do("SORT", v.key, "GET", "*"))
		if err != nil {
			return err
		}
		newVal := reflect.MakeSlice(v.sType, len(reply), len(reply))
		for i, data := range reply {
			elem := reflect.New(v.eType.Elem())
			data, err := redis.Bytes(data, nil)
			if err != nil {
				return nil
			}
			err = GobDecode(data, elem.Interface())
			if err != nil {
				return err
			}
			newVal.Index(i).Set(elem)
		}
		v.slice.Set(newVal)
	default:
		reply, err := redis.Values(r.Do("SMEMBERS", v.key))
		if err != nil {
			return err
		}
		newVal := reflect.MakeSlice(v.sType, len(reply), len(reply))
		eType := v.eType
		if eType.Kind() == reflect.Ptr {
			eType = eType.Elem()
		}
		for i := 0; len(reply) > 0; i++ {
			elem := reflect.New(v.eType)
			reply, err = redis.Scan(reply, elem.Interface())
			if err != nil {
				return err
			}
			if eType.Kind() != reflect.Ptr {
				elem = elem.Elem()
			}
			newVal.Index(i).Set(elem)
		}
		v.slice.Set(newVal)
	}
	return nil
}

func (v *RedisSlice) RedisRemove(r redis.Conn) error {
	if v.slice.Len() == 0 {
		return nil
	}
	keys := make([]interface{}, v.slice.Len()+1)
	keys[0] = v.key
	for i := 0; i < v.slice.Len(); i++ {
		switch ro := v.slice.Index(i).Interface().(type) {
		case RedisInterface:
			keys[i+1] = ro.GetKey()
			RedisRemoveWithConn(r, ro)
		default:
			keys[i+1] = ro
		}
	}
	_, err := r.Do("SREM", keys...)
	return err
}

type RedisNumber struct {
	Key string
}

func (v *RedisNumber) Get() int64 {
	r := pool.Get()
	defer r.Close()

	reply, err := r.Do("GET", v.Key)
	if err != nil {
		fmt.Println("RedisNumber: Get Error", err)
		return 0
	}
	switch reply := reply.(type) {
	case int64:
		return reply
	case []byte:
		n, _ := strconv.ParseInt(string(reply), 10, 64)
		return n
	default:
		fmt.Println("RedisNumber: Get Error", reply)
		return 0
	}
	return 0
}

func (v *RedisNumber) Incr() int64 {
	r := pool.Get()
	defer r.Close()

	reply, err := r.Do("INCR", v.Key)
	if err != nil {
		fmt.Println("RedisNumber: Incr Error", err)
		return 0
	}
	switch reply := reply.(type) {
	case int64:
		return reply
	case []byte:
		n, _ := strconv.ParseInt(string(reply), 10, 64)
		return n
	default:
		fmt.Println("RedisNumber: Incr Error", reply)
		return 0
	}
	return 0
}
