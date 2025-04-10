# CCDC Scorebot SOCKS5 checks proxy

Scoring checks are a difficult thing to manage at CCDC as a Black Team.
You need to make sure that teams aren't just allowing the scoring engine IP address
and blocking everything else. This project is an attempt to randomize the IP address
that the scoring checks come from.

This setup uses the `AnyIP` setup for Linux to bind to any IP address with the `IP_FREEBIND`
options to use a un-assigned, un-aliased IP address as a source IP while still being able
to perform TCP or UDP connections.

## Host Setup

Routing and switching needs to be setup. This doesn't magically make that happen,
but if you assign a very large range (for example 10.1.0.0 - 10.100.255.255) to a
switch / route, this SOCKS5 setup can randomize every connection that comes out of it.

1. First you need to to break down the range into CIDR ranges. This can be done with `netmask`

```
# netmask 10.1.0.0:10.100.255.255
       10.1.0.0/16
       10.2.0.0/15
       10.4.0.0/14
       10.8.0.0/13
      10.16.0.0/12
      10.32.0.0/11
      10.64.0.0/11
      10.96.0.0/14
     10.100.0.0/16
```

2. Next you set [AnyIP](https://blog.widodh.nl/2016/04/anyip-bind-a-whole-subnet-to-your-linux-machine/) ranges into your routing table:

```
ip -4 route add local 10.1.0.0/16 dev lo
ip -4 route add local 10.1.0.0/16 dev lo
ip -4 route add local 10.2.0.0/15 dev lo
ip -4 route add local 10.4.0.0/14 dev lo
ip -4 route add local 10.8.0.0/13 dev lo
ip -4 route add local 10.16.0.0/12 dev lo
ip -4 route add local 10.32.0.0/11 dev lo
ip -4 route add local 10.64.0.0/11 dev lo
ip -4 route add local 10.96.0.0/14 dev lo
ip -4 route add local 10.100.0.0/16 dev lo
```

In order to delete the ranges if you mess them up or just want to stand the proxy down you can see them all with this command:
- `ip route show table all`

Then just `ip route del` + the full line you want to remove

## Building the Proxy

1. `git clone https://github.com/mubix/scoreproxy`
2. `cd scoreproxy`
3. `go build`

I haven't tested different versions of Golang, but https://github.com/armon/go-socks5 hasn't been updated in 9 years,
so it should support most versions that you would want to use.

## Run the Proxy

Help text:
```
Usage of ./scoreproxy:
  -end string
        End IP of the range (e.g., 10.100.255.255)
  -port int
        Port on which the SOCKS5 proxy will listen (default 1080)
  -start string
        Start IP of the range (e.g., 10.1.0.0)
```

Running: 
```
./scoreproxy -start 10.1.0.1 -end 10.100.255.254
2025/04/09 23:26:16 Using IP range from 10.1.0.1 (167837697) to 10.100.255.254 (174391294)
2025/04/09 23:26:16 Starting SOCKS5 server on 0.0.0.0:1080
```

## Let the Proxying Begin

`proxychains4 curl http://10.200.10.10` comes from 10.1.5.33
and then right after comes from 10.4.2.5.


# The Problem

The big problem with this example is that you might hit IP addresses that the routes above deem as unusable but the SOCKS5 proxy might use them, so it's best to try to stay with valid IP ranges. So for the example above, instead of using a bunch of ranges, just using 10.0.0.0/9 which includes 10.0.0.0-10.127.255.255 will and using the 10.0.0.1 start and 10.127.255.254 end in the scoreproxy arguments is the best option.

## Further Research

It looks like `tun2socks` could be a great option to enhance this setup by not having to rely on proxychains to proxy checks or connections:
- https://github.com/ambrop72/badvpn/tree/master/tun2socks
- https://github.com/xjasonlyu/tun2socks

## Source references:
- AnyIP - https://blog.widodh.nl/2016/04/anyip-bind-a-whole-subnet-to-your-linux-machine/
- IP_FREEBIND - https://oswalt.dev/2022/02/non-local-address-binds-in-linux/
- Go SOCKS5 - https://github.com/armon/go-socks5
