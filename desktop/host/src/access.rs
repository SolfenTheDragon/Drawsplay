//! Which network addresses may even start a handshake.
//!
//! `dd-host` listens on all interfaces by default, and a paired client can type on the computer, so
//! a connection from the open internet should never get as far as the pairing question. By default only
//! loopback, private (RFC 1918), link-local, carrier-grade NAT (100.64.0.0/10, which Tailscale uses)
//! and IPv6 unique-local addresses are served; `--allow NET` adds a network and `--allow-any` removes
//! the check.

use std::net::IpAddr;

/// An address range such as `203.0.113.0/24`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Net {
    addr: IpAddr,
    prefix: u8,
}

impl Net {
    /// `ADDR/LEN`, or a bare address for a single host.
    pub fn parse(text: &str) -> Result<Net, String> {
        let (addr, prefix) = match text.split_once('/') {
            Some((a, p)) => (a, Some(p)),
            None => (text, None),
        };
        let addr: IpAddr = addr.trim().parse().map_err(|_| format!("{text}: not an IP address or network"))?;
        let addr = addr.to_canonical();
        let max = if addr.is_ipv4() { 32 } else { 128 };
        let prefix = match prefix {
            Some(p) => p.trim().parse::<u8>().ok().filter(|&p| p <= max).ok_or(format!("{text}: bad prefix length"))?,
            None => max,
        };
        Ok(Net { addr, prefix })
    }

    fn of(addr: &str, prefix: u8) -> Net {
        Net { addr: addr.parse().expect("built-in network"), prefix }
    }

    pub fn contains(&self, ip: IpAddr) -> bool {
        match (self.addr, ip.to_canonical()) {
            (IpAddr::V4(n), IpAddr::V4(a)) => {
                let mask = if self.prefix == 0 { 0 } else { u32::MAX << (32 - u32::from(self.prefix)) };
                u32::from(n) & mask == u32::from(a) & mask
            }
            (IpAddr::V6(n), IpAddr::V6(a)) => {
                let mask = if self.prefix == 0 { 0 } else { u128::MAX << (128 - u32::from(self.prefix)) };
                u128::from(n) & mask == u128::from(a) & mask
            }
            _ => false,
        }
    }
}

pub struct Allow {
    any: bool,
    nets: Vec<Net>,
}

impl Allow {
    /// The private networks plus `extra`; `any` serves every address.
    pub fn new(any: bool, extra: &[Net]) -> Allow {
        let mut nets = vec![
            Net::of("127.0.0.0", 8),
            Net::of("10.0.0.0", 8),
            Net::of("172.16.0.0", 12),
            Net::of("192.168.0.0", 16),
            Net::of("169.254.0.0", 16),
            Net::of("100.64.0.0", 10),
            Net::of("::1", 128),
            Net::of("fc00::", 7),
            Net::of("fe80::", 10),
        ];
        nets.extend_from_slice(extra);
        Allow { any, nets }
    }

    pub fn permits(&self, ip: IpAddr) -> bool {
        self.any || self.nets.iter().any(|n| n.contains(ip))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ip(s: &str) -> IpAddr {
        s.parse().unwrap()
    }

    #[test]
    fn private_networks_are_served_and_the_internet_is_not() {
        let a = Allow::new(false, &[]);
        for ok in [
            "127.0.0.1",
            "10.1.2.3",
            "172.16.0.1",
            "172.31.255.255",
            "192.168.1.50",
            "169.254.9.9",
            "100.64.0.1",
            "100.127.255.255",
            "::1",
            "fd12:3456::1",
            "fe80::1",
            "::ffff:192.168.1.5",
        ] {
            assert!(a.permits(ip(ok)), "{ok} should be served");
        }
        for bad in [
            "8.8.8.8",
            "172.32.0.1",
            "172.15.255.255",
            "100.128.0.1",
            "100.63.255.255",
            "203.0.113.7",
            "2001:db8::1",
            "::ffff:8.8.8.8",
        ] {
            assert!(!a.permits(ip(bad)), "{bad} should be refused");
        }
    }

    #[test]
    fn extra_networks_and_allow_any() {
        let extra = [Net::parse("203.0.113.0/24").unwrap(), Net::parse("198.51.100.9").unwrap()];
        let a = Allow::new(false, &extra);
        assert!(a.permits(ip("203.0.113.200")) && a.permits(ip("198.51.100.9")));
        assert!(!a.permits(ip("203.0.114.1")) && !a.permits(ip("198.51.100.10")));
        assert!(Allow::new(true, &[]).permits(ip("8.8.8.8")));
    }

    #[test]
    fn parsing_rejects_nonsense() {
        for bad in ["", "abc", "1.2.3.4/33", "1.2.3.4/x", "::1/129", "1.2.3/24"] {
            assert!(Net::parse(bad).is_err(), "{bad:?}");
        }
        assert!(Net::parse("0.0.0.0/0").unwrap().contains(ip("8.8.8.8")));
        assert!(!Net::parse("10.0.0.0/8").unwrap().contains(ip("2001:db8::1")));
    }
}
