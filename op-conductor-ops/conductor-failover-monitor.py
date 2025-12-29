#!/usr/bin/env python
"""
Conductor Failover Monitor

Monitors the health status of voting=true nodes. When all voters are unhealthy,
automatically promotes a voting=false node to voter.
"""

import os
import sys
import time
import logging
import argparse
from concurrent.futures import ThreadPoolExecutor, as_completed

import requests
import toml

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
logger = logging.getLogger(__name__)


def make_rpc_payload(method: str, params: list = None):
    """Construct JSON-RPC request payload"""
    if params is None:
        params = []
    return {
        "id": 1,
        "jsonrpc": "2.0",
        "method": method,
        "params": params,
    }


def load_config(config_path: str):
    """Load configuration file"""
    config = toml.load(config_path)

    cert_path = config.get("cert_path", "")
    if cert_path and not cert_path.startswith("/"):
        cert_path = f"{config_path.rsplit('/', 1)[0]}/{cert_path}"

    return config, cert_path


def check_sequencer_healthy(conductor_rpc_url: str, timeout: float = 5.0) -> bool:
    """Check if sequencer is healthy"""
    try:
        resp = requests.post(
            conductor_rpc_url,
            json=make_rpc_payload("conductor_sequencerHealthy"),
            timeout=timeout,
        )
        resp.raise_for_status()
        result = resp.json().get("result")
        return result is True
    except Exception as e:
        logger.debug(f"Failed to check health for {conductor_rpc_url}: {e}")
        return False


def check_conductor_leader(conductor_rpc_url: str, timeout: float = 5.0) -> bool:
    """Check if conductor is the leader"""
    try:
        resp = requests.post(
            conductor_rpc_url,
            json=make_rpc_payload("conductor_leader"),
            timeout=timeout,
        )
        resp.raise_for_status()
        result = resp.json().get("result")
        return result is True
    except Exception as e:
        logger.debug(f"Failed to check leader status for {conductor_rpc_url}: {e}")
        return False


def send_add_voter_request(conductor_rpc_url: str, sequencer_id: str, raft_addr: str, timeout: float = 10.0):
    """Send addServerAsVoter request to a single conductor"""
    try:
        resp = requests.post(
            conductor_rpc_url,
            json=make_rpc_payload(
                "conductor_addServerAsVoter",
                params=[sequencer_id, raft_addr, 0],
            ),
            timeout=timeout,
        )
        resp.raise_for_status()
        json_resp = resp.json()
        if "error" in json_resp:
            return (False, json_resp["error"])
        return (True, json_resp.get("result"))
    except Exception as e:
        return (False, str(e))


def flood_add_voter_to_all(voters: list, target_sequencer_id: str, target_raft_addr: str):
    """
    Flood addServerAsVoter request to all voter nodes.
    Returns: (success: bool, message: str)
    """
    conductor_urls = [v["conductor_rpc_url"] for v in voters]

    logger.info(f"Flooding addServerAsVoter request to {len(conductor_urls)} voter nodes...")

    with ThreadPoolExecutor(max_workers=len(conductor_urls)) as executor:
        futures = {
            executor.submit(
                send_add_voter_request,
                url,
                target_sequencer_id,
                target_raft_addr
            ): url
            for url in conductor_urls
        }

        success = False
        errors = []
        for future in as_completed(futures):
            url = futures[future]
            ok, result = future.result()
            if ok:
                success = True
                logger.debug(f"Request to {url} succeeded: {result}")
            else:
                errors.append(f"{url}: {result}")
                logger.debug(f"Request to {url} failed: {result}")

        if success:
            return (True, "At least one node accepted the request")
        else:
            return (False, f"All nodes failed: {errors}")


def run_monitor(config_path: str, check_interval: int = 10, promote_retry_interval: int = 2, max_retries: int = 30):
    """
    Run the monitoring loop.

    Args:
        config_path: Path to configuration file
        check_interval: Health check interval in seconds
        promote_retry_interval: Promote retry interval in seconds
        max_retries: Maximum number of promote retries
    """
    config, cert_path = load_config(config_path)

    # Set up certificate
    if cert_path:
        os.environ["REQUESTS_CA_BUNDLE"] = cert_path
        os.environ["SSL_CERT_FILE"] = cert_path
        logger.info(f"Using certificate: {cert_path}")

    # Parse configuration
    sequencers_config = config["sequencers"]
    networks_config = config["networks"]

    # Find the first network (assuming only one network)
    network_name = list(networks_config.keys())[0]
    network_sequencer_ids = networks_config[network_name]["sequencers"]

    # Separate voters and non-voters
    voters = []
    non_voters = []

    for seq_id in network_sequencer_ids:
        seq_config = sequencers_config[seq_id]
        seq_info = {
            "sequencer_id": seq_id,
            "raft_addr": seq_config["raft_addr"],
            "conductor_rpc_url": seq_config["conductor_rpc_url"],
            "node_rpc_url": seq_config["node_rpc_url"],
            "voting": seq_config["voting"],
        }
        if seq_config["voting"]:
            voters.append(seq_info)
        else:
            non_voters.append(seq_info)

    logger.info(f"Network: {network_name}")
    logger.info(f"Voters ({len(voters)}): {[v['sequencer_id'] for v in voters]}")
    logger.info(f"Non-voters ({len(non_voters)}): {[v['sequencer_id'] for v in non_voters]}")

    if not non_voters:
        logger.error("No voting=false node available for promotion, exiting")
        sys.exit(1)

    # Select the first non-voter as the promote target
    target = non_voters[0]
    logger.info(f"Promote target: {target['sequencer_id']}")

    logger.info(f"Starting monitor, check interval: {check_interval}s")
    logger.info("-" * 50)

    while True:
        try:
            # Check health status of all voters
            logger.info("Checking voters health status...")

            healthy_voters = []
            unhealthy_voters = []

            for voter in voters:
                is_healthy = check_sequencer_healthy(voter["conductor_rpc_url"])
                if is_healthy:
                    healthy_voters.append(voter["sequencer_id"])
                else:
                    unhealthy_voters.append(voter["sequencer_id"])

            logger.info(f"Healthy: {healthy_voters or 'None'}")
            logger.info(f"Unhealthy: {unhealthy_voters or 'None'}")

            # If all voters are unhealthy, execute promote
            if len(healthy_voters) == 0:
                logger.warning("⚠️  All voters are unhealthy! Starting to promote non-voter...")

                retry_count = 0
                promote_success = False

                while retry_count < max_retries and not promote_success:
                    retry_count += 1
                    logger.info(f"Promote attempt #{retry_count}/{max_retries}")

                    # Flood addServerAsVoter to all voters
                    success, msg = flood_add_voter_to_all(
                        voters,
                        target["sequencer_id"],
                        target["raft_addr"],
                    )

                    if success:
                        logger.info(f"addServerAsVoter request sent: {msg}")
                    else:
                        logger.warning(f"addServerAsVoter request failed: {msg}")

                    # Wait a moment for raft election to complete
                    time.sleep(1)

                    # Check if target has become leader
                    is_leader = check_conductor_leader(target["conductor_rpc_url"])

                    if is_leader:
                        logger.info(f"✅ Promote successful! {target['sequencer_id']} is now leader")
                        logger.info("Failover complete, exiting")
                        sys.exit(0)
                    else:
                        logger.warning(f"❌ {target['sequencer_id']} is not leader yet, retrying in {promote_retry_interval}s...")
                        time.sleep(promote_retry_interval)

                if not promote_success:
                    logger.error(f"Promote failed! Attempted {max_retries} times, exiting")
                    sys.exit(1)

                logger.info("-" * 50)
            else:
                logger.info(f"✅ {len(healthy_voters)} voter(s) healthy, no action needed")

            logger.info(f"Waiting {check_interval}s before next check...")
            logger.info("-" * 50)
            time.sleep(check_interval)

        except KeyboardInterrupt:
            logger.info("Received interrupt signal, exiting monitor")
            break
        except Exception as e:
            logger.error(f"Monitor loop error: {e}")
            time.sleep(check_interval)


def main():
    parser = argparse.ArgumentParser(
        description="Conductor Failover Monitor - Monitor voters health and auto-promote non-voter"
    )
    parser.add_argument(
        "-c", "--config",
        default="./config.toml",
        help="Path to config file (default: ./config.toml)",
    )
    parser.add_argument(
        "-i", "--interval",
        type=int,
        default=10,
        help="Health check interval in seconds (default: 10)",
    )
    parser.add_argument(
        "--promote-retry-interval",
        type=int,
        default=2,
        help="Promote retry interval in seconds (default: 2)",
    )
    parser.add_argument(
        "--max-retries",
        type=int,
        default=30,
        help="Maximum promote retry attempts (default: 30)",
    )
    parser.add_argument(
        "-v", "--verbose",
        action="store_true",
        help="Enable verbose logging",
    )
    parser.add_argument(
        "--cert",
        default="",
        help="SSL certificate file path",
    )

    args = parser.parse_args()

    if args.verbose:
        logging.getLogger().setLevel(logging.DEBUG)

    if args.cert:
        os.environ["REQUESTS_CA_BUNDLE"] = args.cert
        os.environ["SSL_CERT_FILE"] = args.cert

    logger.info("=" * 50)
    logger.info("Conductor Failover Monitor Starting")
    logger.info("=" * 50)

    run_monitor(
        config_path=args.config,
        check_interval=args.interval,
        promote_retry_interval=args.promote_retry_interval,
        max_retries=args.max_retries,
    )


if __name__ == "__main__":
    main()
