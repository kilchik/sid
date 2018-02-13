#!/usr/bin/env python

import os
import plotly.plotly as py
import plotly.graph_objs as go
import pandas as pd
import sys


def write_error(msg):
    sys.stderr.write("[E] %s\n" % msg)


def main():
    if len(sys.argv) != 2:
        write_error("invalid number of arguments")
        return 1

    csv_path = sys.argv[1]
    if not os.path.exists(csv_path):
        write_error("no such file: %s" % csv_path)
        return 1

    # expected filename is "Name-Timestamp.csv"
    name, _ = os.path.splitext(os.path.basename(csv_path))

    try:
        py.sign_in("DemoAccount", "2qdyfjyr7o")
        df = pd.read_csv(csv_path)

        trace = go.Table(
            header=dict(values=df.columns,
                        fill = dict(color='#C2D4FF'),
                        align = ['left'] * 5),
            cells=dict(values=[df.Title, df.Amount, df.Payer, df.Date],
                       fill = dict(color='#F5F8FF'),
                       align = ['left'] * 5))

        data = [trace]
        py.image.save_as(data, filename="%s.png" % name, scale=2)

    except Exception as e:
        write_error("caugth exception: %s" % e.message)
        return 1


if __name__ == "__main__":
    sys.exit(main())
