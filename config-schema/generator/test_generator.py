#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
test_generator.py — schema generator 单元测试（CI 与本地共用）。
用内嵌的最小 .conf.dist 样本验证：分节识别、多 key 共享说明、
类型推断、重复 key 忽略、Default 行尾注释剥离。
"""
import json
import os
import sys
import tempfile

sys.path.insert(0, os.path.dirname(__file__))
from generate import parse_dist, infer_type  # noqa: E402

SAMPLE = """################################################
# Test config                                  #
################################################
[worldserver]

#################################################################
# DATABASE & CONNECTIONS
#
#    RealmID
#        Description: ID of the Realm using this config.
#        Important:   must match auth db.
#        Default:     1

RealmID = 1

#
#    WorldServerPort
#        Description: TCP port to reach the world server.
#        Default:     8085

WorldServerPort = 8085

#
#    BindIP
#        Description: Bind world server to IP/hostname
#        Default:     "0.0.0.0" - (Bind to all IPs on the system)

BindIP = "0.0.0.0"

#
#    LoginDatabaseInfo
#    WorldDatabaseInfo
#        Description: Database connection settings.
#        Default:     "127.0.0.1;3306;acore;acore;acore_auth"

LoginDatabaseInfo     = "127.0.0.1;3306;acore;acore;acore_auth"
WorldDatabaseInfo     = "127.0.0.1;3306;acore;acore;acore_world"

#
#    Rate.XP.Kill
#        Description: Kill XP rate.
#        Default:     1 - (rate 1)

Rate.XP.Kill = 1.5

#
#    Rate.XP.Kill
#        Description: duplicated key, must be ignored.

Rate.XP.Kill = 9

TerrainMapRate = 1
"""


def main() -> int:
    with tempfile.NamedTemporaryFile("w", suffix=".conf.dist", delete=False, encoding="utf-8") as f:
        f.write(SAMPLE)
        path = f.name
    try:
        s = parse_dist(path)
    finally:
        os.unlink(path)

    entries = {e["key"]: e for e in s["entries"]}

    # 基本存在性
    for k in ("RealmID", "WorldServerPort", "BindIP", "LoginDatabaseInfo",
              "WorldDatabaseInfo", "Rate.XP.Kill", "TerrainMapRate"):
        assert k in entries, f"missing key: {k}"

    # 分组
    assert entries["RealmID"]["section"] == "DATABASE & CONNECTIONS", entries["RealmID"]["section"]

    # 类型推断
    assert entries["RealmID"]["type"] == "int"
    assert entries["BindIP"]["type"] == "string"
    assert entries["Rate.XP.Kill"]["type"] == "int"  # default 行推导，而非实际值
    assert infer_type("1.5") == "float"
    assert infer_type('"0.0.0.0"') == "string"

    # Default 行的尾注释剥离
    assert entries["BindIP"]["default"] == "0.0.0.0", entries["BindIP"]["default"]

    # 多 key 共享说明
    assert entries["WorldDatabaseInfo"]["description"] == "Database connection settings."

    # 重复 key 忽略（首次声明生效）
    assert entries["Rate.XP.Kill"]["default"] == "1"

    # 无说明的孤立设置
    assert entries["TerrainMapRate"]["section"] == "DATABASE & CONNECTIONS"

    print("config-schema generator tests: OK")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
