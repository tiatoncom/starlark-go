# Sample program of every conditional form the compiler knows.
# Its bytecode is pinned by TestCodeBytesGolden against the v0.2.0 compiler:
# trace marks must never change a byte of Funcode.Code.
# option:while
# option:set

def plain(a):
    if a:
        return 1
    return 0

def not_cond(a):
    if not a:
        return 1
    if not not a:
        return 2
    return 0

def ladder(a):
    if a == 1:
        return "one"
    elif a == 2:
        return "two"
    elif a == 3:
        return "three"
    else:
        return "many"

def else_if(a):
    if a == 1:
        return "one"
    else:
        if a == 2:
            return "two"
    return "other"

def loop(n):
    i = 0
    while i < n:
        i += 1
    return i

def ternary(a, b):
    return a if b else (b if a else None)

def comp(xs):
    return [x for x in xs if x > 1 if x != 5]

def chains(a, b, c, d):
    if a and b and c:
        return 1
    if a or b or c:
        return 2
    if a and b or c:
        return 3
    if a or b and c:
        return 4
    if a and (b or c):
        return 5
    if not (a and b):
        return 6
    if (a and b) or (c and d):
        return 7
    if a and not b:
        return 8
    if not a and b:
        return 9
    if a == 1 and b != 2 or c in [1, 2] and d not in [3]:
        return 10
    return 0

def paren(a, b):
    if (a):
        return 1
    if (a and b):
        return 2
    if (a == b):
        return 3
    return 0

def value_ctx(a, b, c):
    x = a and b
    y = a or b and c
    z = [a and b for _ in range(2)]
    return x, y, z

def in_call(f, a, b):
    if f(a and b):
        return 1
    return 0

def nested_cond(a, b, c):
    if (a if b else c) and c:
        return 1
    while a and b:
        a = False
    return 0

g = lambda a, b: a and b
h = lambda a: 1 if a else 2
