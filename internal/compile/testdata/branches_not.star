# Conditions in which a "not" or a chain stands where the compiler evaluates
# it as a value, and comparisons at every place of a chain. Their bytecode is
# pinned by TestCodeBytesGoldenNot against the v0.2.0 compiler: the marks of
# the branch table must never change a byte of Funcode.Code.
# option:while
# option:set

def not_chains(a, b, c, d):
    if not (a and b) and c:
        return 1
    if (a and not (b and c)):
        return 2
    if (not a or not (b and c)):
        return 3
    if not (not (a and b)):
        return 4
    if not not (a and b) and not not c:
        return 5
    if a and not (b or c) and d:
        return 6
    if not (a or b) or c:
        return 7
    if not (a and b) or not (c and d):
        return 8
    return 0

def not_values(a, b, xs):
    if not a and b:
        return 1
    if a not in xs and b:
        return 2
    if not a == 1 and b:
        return 3
    if (a not in xs or b) and a:
        return 4
    if not (a not in xs):
        return 5
    if a and not (b not in xs) and a:
        return 6
    return 0

def compare_places(a, c, d, xs):
    if a == 1 and d and d:
        return 1
    if d and a < 2 and d:
        return 2
    if d and d and a in xs:
        return 3
    if a not in xs or c or c:
        return 4
    if c or a not in xs or c:
        return 5
    if c or c or not a == 1:
        return 6
    if (c or (a == 1 and d)) and d:
        return 7
    if d and not (d and a not in xs):
        return 8
    return 0

def chain_ids(a, b, c, d):
    if (a and b) and c:
        return 1
    if ((a and b) and c) and d:
        return 2
    if a and b and c and d:
        return 3
    x = (1 if (a and b) else 2) if (c and d) else 3
    return x
